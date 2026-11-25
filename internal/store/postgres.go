package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/model"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const (
	// Verrou consultatif pris pendant les migrations : si plusieurs réplicas
	// démarrent en même temps (Container Apps), un seul migre, les autres attendent.
	migrationLockID = 7302215001
	queryTimeout    = 5 * time.Second
	connectWait     = 60 * time.Second
)

// Postgres est le Store persistant (PostgreSQL 16, local ou Azure Flexible Server).
// Toutes les requêtes sont paramétrées : aucune valeur n'est concaténée dans le SQL.
type Postgres struct {
	pool *pgxpool.Pool
}

// OpenPostgres se connecte (en attendant que la base soit prête) puis applique
// les migrations manquantes. Le DSN n'est jamais journalisé : il contient le mot de passe.
func OpenPostgres(ctx context.Context, dsn string, log *slog.Logger) (*Postgres, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("DATABASE_URL invalide") // l'erreur d'origine pourrait citer le DSN
	}
	cfg.MaxConns = 5
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	cfg.ConnConfig.RuntimeParams["application_name"] = "containermaxxing"

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("postgres: %w", err)
	}
	deadline := time.Now().Add(connectWait)
	for {
		pingCtx, cancel := context.WithTimeout(ctx, queryTimeout)
		err = pool.Ping(pingCtx)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			pool.Close()
			return nil, fmt.Errorf("postgres injoignable: %w", err)
		}
		log.Warn("postgres pas encore prêt, nouvel essai dans 2 s", "err", err)
		time.Sleep(2 * time.Second)
	}

	p := &Postgres{pool: pool}
	if err := p.migrate(ctx, log); err != nil {
		pool.Close()
		return nil, err
	}
	return p, nil
}

func (p *Postgres) Close() { p.pool.Close() }

// migrate applique, dans l'ordre, les fichiers migrations/*.sql pas encore appliqués.
// Tout se fait dans une seule transaction : en cas d'erreur, rien n'est appliqué.
func (p *Postgres) migrate(ctx context.Context, log *slog.Logger) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("migrations: verrou: %w", err)
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("migrations: %w", err)
	}

	applied := map[string]bool{}
	rows, err := tx.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return fmt.Errorf("migrations: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("migrations: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations") // triés par nom
	if err != nil {
		return err
	}
	for _, e := range entries {
		version := strings.TrimSuffix(e.Name(), ".sql")
		if applied[version] {
			continue
		}
		sql, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return err
		}
		// Sans paramètre, pgx utilise le protocole simple : plusieurs instructions possibles.
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("migration %s: %w", version, err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version); err != nil {
			return fmt.Errorf("migration %s: %w", version, err)
		}
		log.Info("migration appliquée", "version", version)
	}
	return tx.Commit(ctx)
}

const deploymentColumns = `id, tag, digest, status, cve_critical, cve_high, cve_medium,
	dockle_status, signed, target, run_url, started_at, duration_s`

func scanDeployment(row pgx.Row) (model.Deployment, error) {
	var d model.Deployment
	var status string
	err := row.Scan(&d.ID, &d.Tag, &d.Digest, &status, &d.CVECritical, &d.CVEHigh, &d.CVEMedium,
		&d.DockleStatus, &d.Signed, &d.Target, &d.RunURL, &d.StartedAt, &d.DurationS)
	d.Status = model.Status(status)
	return d, err
}

func (p *Postgres) ListDeployments(ctx context.Context, limit int) ([]model.Deployment, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	rows, err := p.pool.Query(ctx,
		"SELECT "+deploymentColumns+" FROM deployments ORDER BY started_at DESC, id DESC LIMIT $1", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Deployment{}
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (p *Postgres) GetDeployment(ctx context.Context, id int64) (model.Deployment, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	d, err := scanDeployment(p.pool.QueryRow(ctx,
		"SELECT "+deploymentColumns+" FROM deployments WHERE id = $1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Deployment{}, ErrNotFound
	}
	if err != nil {
		return model.Deployment{}, err
	}
	rows, err := p.pool.Query(ctx, `SELECT name, status, started_at, duration_s
		FROM deployment_steps WHERE deployment_id = $1 ORDER BY position`, id)
	if err != nil {
		return model.Deployment{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var s model.DeploymentStep
		var status string
		if err := rows.Scan(&s.Name, &status, &s.StartedAt, &s.DurationS); err != nil {
			return model.Deployment{}, err
		}
		s.Status = model.Status(status)
		d.Steps = append(d.Steps, s)
	}
	return d, rows.Err()
}

// CreateDeployment insère le déploiement et ses steps dans une même transaction.
func (p *Postgres) CreateDeployment(ctx context.Context, d model.Deployment) (model.Deployment, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return d, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	err = tx.QueryRow(ctx, `INSERT INTO deployments
		(tag, digest, status, cve_critical, cve_high, cve_medium, dockle_status, signed, target, run_url, started_at, duration_s)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id`,
		d.Tag, d.Digest, string(d.Status), d.CVECritical, d.CVEHigh, d.CVEMedium,
		d.DockleStatus, d.Signed, d.Target, d.RunURL, d.StartedAt, d.DurationS).Scan(&d.ID)
	if err != nil {
		return d, err
	}
	if len(d.Steps) > 0 {
		rows := make([][]any, len(d.Steps))
		for i, s := range d.Steps {
			rows[i] = []any{d.ID, int16(i), s.Name, string(s.Status), s.StartedAt, s.DurationS}
		}
		_, err = tx.CopyFrom(ctx, pgx.Identifier{"deployment_steps"},
			[]string{"deployment_id", "position", "name", "status", "started_at", "duration_s"},
			pgx.CopyFromRows(rows))
		if err != nil {
			return d, err
		}
	}
	return d, tx.Commit(ctx)
}

// Stats agrège côté base plutôt que de rapatrier tout l'historique.
func (p *Postgres) Stats(ctx context.Context) (model.Stats, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()
	s := model.Stats{ByTarget: map[string]int{}}
	var signed int
	err := p.pool.QueryRow(ctx, `SELECT
			count(*),
			count(*) FILTER (WHERE status = 'success'),
			count(*) FILTER (WHERE status = 'failed'),
			coalesce(avg(duration_s) FILTER (WHERE status = 'success'), 0),
			count(*) FILTER (WHERE signed),
			max(started_at)
		FROM deployments`).Scan(&s.Total, &s.Succeeded, &s.Failed, &s.AvgDurationS, &signed, &s.LastDeployAt)
	if err != nil {
		return s, err
	}
	if s.Total > 0 {
		s.FailureRate = float64(s.Failed) / float64(s.Total)
		s.SignedRate = float64(signed) / float64(s.Total)
	}
	rows, err := p.pool.Query(ctx, "SELECT target, count(*) FROM deployments GROUP BY target")
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var target string
		var n int
		if err := rows.Scan(&target, &n); err != nil {
			return s, err
		}
		s.ByTarget[target] = n
	}
	return s, rows.Err()
}
