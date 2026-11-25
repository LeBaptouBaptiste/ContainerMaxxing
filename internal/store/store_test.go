package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/cache"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/model"
)

// Contrat commun : Memory et Postgres doivent se comporter de la même façon.
func testStoreContract(t *testing.T, s Store) {
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	digest := "sha256:" + strings.Repeat("ab", 32)

	if _, err := s.GetDeployment(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetDeployment(999) = %v, want ErrNotFound", err)
	}
	empty, err := s.Stats(ctx)
	if err != nil || empty.Total != 0 || empty.LastDeployAt != nil {
		t.Fatalf("stats vides = %+v, %v", empty, err)
	}

	mk := func(tag, target string, status model.Status, at time.Time, dur float64, signed bool) model.Deployment {
		return model.Deployment{
			Tag: tag, Digest: digest, Status: status, CVEMedium: 2, DockleStatus: "pass",
			Signed: signed, Target: target, StartedAt: at, DurationS: dur,
			Steps: []model.DeploymentStep{
				{Name: "Build", Status: model.StatusSuccess, StartedAt: at, DurationS: dur / 2},
				{Name: "Trivy", Status: status, StartedAt: at.Add(time.Minute), DurationS: dur / 2},
				{Name: "Deploy", Status: model.StatusSkipped, StartedAt: at.Add(2 * time.Minute), DurationS: 0},
			},
		}
	}
	a, err := s.CreateDeployment(ctx, mk("v1.0.0", "phobos", model.StatusSuccess, base, 100, false))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateDeployment(ctx, mk("v1.1.0", "azure", model.StatusFailed, base.Add(time.Hour), 60, false))
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.CreateDeployment(ctx, mk("v1.2.0", "azure", model.StatusSuccess, base.Add(2*time.Hour), 200, true))
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == 0 || b.ID <= a.ID || c.ID <= b.ID {
		t.Fatalf("ids = %d, %d, %d", a.ID, b.ID, c.ID)
	}

	list, err := s.ListDeployments(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Tag != "v1.2.0" || list[1].Tag != "v1.1.0" {
		t.Fatalf("liste = %+v", list)
	}
	if list[0].Steps != nil {
		t.Fatal("la liste ne doit pas inclure les steps")
	}

	got, err := s.GetDeployment(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tag != "v1.1.0" || got.Status != model.StatusFailed || !got.StartedAt.Equal(base.Add(time.Hour)) {
		t.Fatalf("get = %+v", got)
	}
	if len(got.Steps) != 3 || got.Steps[0].Name != "Build" || got.Steps[2].Status != model.StatusSkipped {
		t.Fatalf("steps = %+v", got.Steps)
	}

	st, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Total != 3 || st.Succeeded != 2 || st.Failed != 1 || st.AvgDurationS != 150 ||
		st.ByTarget["azure"] != 2 || st.ByTarget["phobos"] != 1 ||
		st.LastDeployAt == nil || !st.LastDeployAt.Equal(base.Add(2*time.Hour)) {
		t.Fatalf("stats = %+v", st)
	}
	if fmt.Sprintf("%.3f", st.FailureRate) != "0.333" || fmt.Sprintf("%.3f", st.SignedRate) != "0.333" {
		t.Fatalf("taux = %v / %v", st.FailureRate, st.SignedRate)
	}
}

func TestMemoryStore(t *testing.T) {
	testStoreContract(t, NewMemory())
}

// Le cache ne doit rien changer au comportement observable : même contrat,
// y compris les écritures visibles immédiatement (invalidation par génération).
func TestCachedStore(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	testStoreContract(t, WithCache(NewMemory(), cache.NewLoader(cache.NewMemory(100), log)))
}

// Cache en panne (Redis arrêté) : l'historique reste servi normalement.
func TestCachedStoreWithBrokenCache(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	testStoreContract(t, WithCache(NewMemory(), cache.NewLoader(failingCache{}, log)))
}

type failingCache struct{}

func (failingCache) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, errors.New("down")
}
func (failingCache) Set(context.Context, string, []byte, time.Duration) error {
	return errors.New("down")
}
func (failingCache) Incr(context.Context, string) (int64, error) { return 0, errors.New("down") }
func (failingCache) Close() error                                { return nil }

// TestPostgresStore tourne seulement si TEST_DATABASE_URL est défini.
// Il travaille dans un schéma temporaire, supprimé à la fin : jamais sur les vraies tables.
func TestPostgresStore(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL non défini")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("test_%d", time.Now().UnixNano())
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })

	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p, err := OpenPostgres(ctx, dsn+sep+"search_path="+schema, log)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	testStoreContract(t, p)

	// Les migrations sont idempotentes : un second démarrage ne réapplique rien.
	p2, err := OpenPostgres(ctx, dsn+sep+"search_path="+schema, log)
	if err != nil {
		t.Fatalf("second démarrage : %v", err)
	}
	p2.Close()

	// Les contraintes de la base refusent une valeur hors domaine, même sans passer par l'API.
	bad := model.Deployment{
		Tag: "v9.9.9", Digest: "sha256:" + strings.Repeat("ab", 32), Status: model.StatusSuccess,
		DockleStatus: "pass", Target: "azure", RunURL: "javascript:alert(1)", StartedAt: time.Now(),
	}
	if _, err := p.CreateDeployment(ctx, bad); err == nil {
		t.Fatal("la base a accepté un run_url javascript:")
	}
}
