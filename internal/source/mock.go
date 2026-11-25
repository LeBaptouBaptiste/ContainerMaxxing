package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/config"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/model"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/store"
)

// Mock simule une chaîne de déploiement vivante : à chaque Poll, le run CI
// en cours avance d'un step. Un run sur quatre échoue au scan Trivy, ce qui
// montre que la prod reste sur la version précédente (quality gate).
type Mock struct {
	mu      sync.Mutex
	cfg     cfg
	store   store.Store
	rng     *rand.Rand
	now     func() time.Time
	version string

	major, minor, patch int
	runCount            int
	idle                int
	run                 *mockRun
	prod                model.ProdInfo
	acr                 []acrEntry // plus récent d'abord
	phobosTag           string
}

type cfg struct {
	env, repo, phobosURL, azureURL, acr, caasApp string
}

type acrEntry struct {
	tag, digest string
	signed      bool
	pushedAt    time.Time
}

type mockRun struct {
	number      int
	tag, digest string
	commit      string
	startedAt   time.Time
	steps       []model.Step
	durations   []float64 // durées simulées, révélées à la fin de chaque step
	cur         int       // index du step en cours ; len(steps) quand terminé
	failAt      int       // -1 si le run réussit
	crit, high  int
	med, low    int
	findings    []model.Finding
	finished    bool
	failed      bool
}

type stepDef struct {
	name     string
	min, max float64
}

var ciSteps = []stepDef{
	{"Checkout", 2, 5},
	{"Build image", 45, 90},
	{"Trivy scan", 20, 40},
	{"Dockle lint", 5, 12},
	{"Push ACR", 10, 25},
	{"Cosign sign", 4, 10},
	{"Deploy Container Apps", 30, 70},
	{"Record deployment", 1, 3},
}

const (
	stepBuild  = 1
	stepTrivy  = 2
	stepDockle = 3
	stepPush   = 4
	stepSign   = 5
	stepDeploy = 6
)

func NewMock(c config.Config, st store.Store, version string) *Mock {
	return &Mock{
		cfg: cfg{
			env: c.Environment, repo: c.GitHubRepo, phobosURL: c.PhobosURL,
			azureURL: c.AzureURL, acr: c.ACRName, caasApp: c.CaaSApp,
		},
		store:   st,
		rng:     rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0x5eed)),
		now:     time.Now,
		version: version,
		idle:    2,
	}
}

// Seed remplit l'historique avec des déploiements fictifs si le store est vide
// et initialise l'état courant (prod, registre) en cohérence.
func (m *Mock) Seed(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	existing, err := m.store.ListDeployments(ctx, 1)
	if err != nil {
		return err
	}
	type seed struct {
		tag, target     string
		crit, high, med int
		dockle          string
		failAtTrivy     bool
	}
	history := []seed{
		{"v1.0.0", "phobos", 1, 6, 14, "warn", false},
		{"v1.0.1", "phobos", 1, 5, 12, "warn", false},
		{"v1.1.0", "phobos", 0, 0, 3, "pass", false},
		{"v1.1.1", "azure", 0, 0, 3, "pass", false},
		{"v1.2.0", "azure", 0, 2, 4, "pass", true},
		{"v1.2.1", "azure", 0, 0, 2, "pass", false},
		{"v1.2.2", "azure", 0, 0, 2, "pass", false},
		{"v1.2.3", "phobos", 0, 0, 2, "pass", false},
		{"v1.3.0", "azure", 1, 1, 3, "pass", true},
		{"v1.3.1", "azure", 0, 0, 1, "pass", false},
		{"v1.3.2", "azure", 0, 0, 1, "pass", false},
	}
	start := m.now().Add(-21 * 24 * time.Hour)
	for i, h := range history {
		at := start.Add(time.Duration(i)*44*time.Hour + time.Duration(m.rng.IntN(300))*time.Minute)
		d := model.Deployment{
			Tag: h.tag, Digest: fakeDigest(h.tag), Target: h.target, StartedAt: at,
			CVECritical: h.crit, CVEHigh: h.high, CVEMedium: h.med, DockleStatus: h.dockle,
			Status: model.StatusSuccess,
		}
		var names []string
		failIdx := -1
		if h.target == "phobos" {
			names = []string{"Build image", "Trivy scan", "Dockle lint", "podman-compose up"}
		} else {
			for _, s := range ciSteps {
				names = append(names, s.name)
			}
			d.RunURL = fmt.Sprintf("https://github.com/%s/actions/runs/%d", m.cfg.repo, 11800000+i*137)
			if h.failAtTrivy {
				failIdx = stepTrivy
			}
		}
		t := at
		for j, n := range names {
			st := model.StatusSuccess
			dur := m.stepDuration(n)
			if failIdx >= 0 && j > failIdx {
				st, dur = model.StatusPending, 0
			} else if j == failIdx {
				st = model.StatusFailed
			}
			d.Steps = append(d.Steps, model.DeploymentStep{Name: n, Status: st, StartedAt: t, DurationS: dur})
			t = t.Add(time.Duration(dur * float64(time.Second)))
			d.DurationS += dur
		}
		d.DurationS = round1(d.DurationS)
		if failIdx >= 0 {
			d.Status = model.StatusFailed
			d.DockleStatus = "skipped"
		}
		d.Signed = h.target == "azure" && failIdx < 0
		if len(existing) == 0 {
			if _, err := m.store.CreateDeployment(ctx, d); err != nil {
				return err
			}
		}
		if h.target == "phobos" {
			m.phobosTag = h.tag
		}
		if h.target == "azure" && !h.failAtTrivy {
			m.prod = model.ProdInfo{Tag: h.tag, Digest: d.Digest, Since: t}
			m.acr = append([]acrEntry{{tag: h.tag, digest: d.Digest, signed: true, pushedAt: at}}, m.acr...)
		}
	}
	m.major, m.minor, m.patch = 1, 3, 2
	m.runCount = 11
	// Le dernier run réussi est affiché comme run « terminé » au démarrage.
	m.run = m.newRun(m.prod.Tag, start.Add(10*44*time.Hour), -1)
	for i := range m.run.steps {
		m.run.steps[i].Status = model.StatusSuccess
		m.run.steps[i].DurationS = m.run.durations[i]
	}
	m.run.cur, m.run.finished = len(m.run.steps), true
	return nil
}

func (m *Mock) Poll(ctx context.Context) (model.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	err := m.advance(ctx)
	return m.snapshot(), err
}

func (m *Mock) advance(ctx context.Context) error {
	now := m.now()
	r := m.run
	if r == nil || r.finished {
		if m.idle > 0 {
			m.idle--
			return nil
		}
		m.startRun(now)
		return nil
	}

	s := &r.steps[r.cur]
	s.DurationS = r.durations[r.cur]
	if r.cur == r.failAt {
		s.Status = model.StatusFailed
		s.Log = r.stepLog(r.cur, true)
		r.finished, r.failed = true, true
		m.idle = 3
		return m.record(ctx, r)
	}
	s.Status = model.StatusSuccess
	s.Log = r.stepLog(r.cur, false)
	switch r.cur {
	case stepPush:
		m.acr = append([]acrEntry{{tag: r.tag, digest: r.digest, pushedAt: now}}, m.acr...)
		m.acr = m.acr[:min(len(m.acr), 8)]
	case stepSign:
		m.acr[0].signed = true
	case stepDeploy:
		m.prod = model.ProdInfo{Tag: r.tag, Digest: r.digest, Since: now}
	}
	r.cur++
	if r.cur == len(r.steps) {
		r.finished = true
		m.idle = 4
		return m.record(ctx, r)
	}
	r.steps[r.cur].Status = model.StatusRunning
	r.steps[r.cur].StartedAt = &now
	return nil
}

func (m *Mock) startRun(now time.Time) {
	m.runCount++
	if m.runCount%3 == 0 {
		m.minor, m.patch = m.minor+1, 0
	} else {
		m.patch++
	}
	tag := fmt.Sprintf("v%d.%d.%d", m.major, m.minor, m.patch)
	failAt := -1
	if m.runCount%4 == 2 {
		failAt = stepTrivy
	}
	m.run = m.newRun(tag, now, failAt)
	m.run.steps[0].Status = model.StatusRunning
	m.run.steps[0].StartedAt = &now
}

func (m *Mock) newRun(tag string, at time.Time, failAt int) *mockRun {
	r := &mockRun{
		number: m.runCount + 30, tag: tag, digest: fakeDigest(tag), commit: fakeDigest("commit" + tag)[7:47],
		startedAt: at, failAt: failAt, med: m.rng.IntN(3), low: 2 + m.rng.IntN(6),
	}
	if failAt == stepTrivy {
		r.high = 1
		r.findings = []model.Finding{{
			ID: "MOCK-CVE-0001", Severity: "HIGH", Target: "golang.org/x/net (gobinary)",
			Title: "Vulnérabilité fictive injectée par le mode mock pour montrer le blocage",
		}}
	}
	for i := 0; i < r.med; i++ {
		r.findings = append(r.findings, model.Finding{
			ID: fmt.Sprintf("MOCK-CVE-01%02d", i+1), Severity: "MEDIUM", Target: "stdlib (gobinary)",
			Title: "Vulnérabilité fictive de sévérité moyenne (non bloquante)",
		})
	}
	for _, s := range ciSteps {
		r.steps = append(r.steps, model.Step{Name: s.name, Status: model.StatusPending})
		r.durations = append(r.durations, m.stepDuration(s.name))
	}
	return r
}

func (m *Mock) stepDuration(name string) float64 {
	for _, s := range ciSteps {
		if s.name == name {
			return round1(s.min + m.rng.Float64()*(s.max-s.min))
		}
	}
	return round1(20 + m.rng.Float64()*20) // podman-compose up
}

func (m *Mock) record(ctx context.Context, r *mockRun) error {
	d := model.Deployment{
		Tag: r.tag, Digest: r.digest, Target: "azure", StartedAt: r.startedAt,
		CVECritical: r.crit, CVEHigh: r.high, CVEMedium: r.med,
		DockleStatus: "pass", Signed: r.cur > stepSign, Status: model.StatusSuccess,
		RunURL: m.runURL(r),
	}
	if r.failed {
		d.Status = model.StatusFailed
		if r.failAt <= stepDockle {
			d.DockleStatus = "skipped"
		}
	}
	t := r.startedAt
	for i, s := range r.steps {
		d.Steps = append(d.Steps, model.DeploymentStep{Name: s.Name, Status: s.Status, StartedAt: t, DurationS: s.DurationS})
		t = t.Add(time.Duration(s.DurationS * float64(time.Second)))
		d.DurationS += r.steps[i].DurationS
	}
	d.DurationS = round1(d.DurationS)
	_, err := m.store.CreateDeployment(ctx, d)
	return err
}

func (m *Mock) runURL(r *mockRun) string {
	return fmt.Sprintf("https://github.com/%s/actions/runs/%d", m.cfg.repo, 11801500+r.number)
}

// --- construction du snapshot ---------------------------------------------

func (m *Mock) snapshot() model.Snapshot {
	now := m.now()
	return model.Snapshot{
		Environment: m.cfg.env,
		Source:      "mock",
		AppVersion:  m.version,
		Prod:        m.prod,
		GeneratedAt: now,
		Stages: []model.Stage{
			m.imageStage(now), m.phobosStage(now), m.composeStage(now),
			m.githubStage(now), m.actionsStage(now), m.acrStage(now), m.caasStage(now),
		},
	}
}

func (m *Mock) imageRef(tag string) string {
	return fmt.Sprintf("%s.azurecr.io/containermaxxing:%s", m.cfg.acr, tag)
}

func (m *Mock) imageStage(now time.Time) model.Stage {
	r := m.run
	st := model.Stage{ID: model.StageImage, Label: "Image", UpdatedAt: now, Status: model.StatusSuccess}
	switch {
	case !r.finished && r.cur <= stepBuild:
		st.Status, st.Summary = model.StatusRunning, "build de "+r.tag
	case !r.finished && r.cur <= stepDockle:
		st.Status, st.Summary = model.StatusRunning, "scan de "+r.tag
	case r.failed && r.failAt <= stepDockle:
		st.Status, st.Summary = model.StatusFailed, r.tag+" bloquée par le scan"
	default:
		st.Summary = r.tag + " · distroless · scannée"
	}
	st.Fields = []model.Field{
		{Label: "Image", Value: m.imageRef(r.tag), Mono: true},
		{Label: "Image de base", Value: "gcr.io/distroless/static:nonroot", Mono: true},
		{Label: "Utilisateur", Value: "65532 (nonroot)"},
		{Label: "Shell", Value: "aucun (distroless)"},
		{Label: "Compilation", Value: "CGO_ENABLED=0 · -trimpath · -ldflags='-s -w'", Mono: true},
		{Label: "Taille", Value: "≈ 9,6 Mo"},
	}
	if r.finished || r.cur > stepBuild {
		st.Fields = append(st.Fields, model.Field{Label: "Digest", Value: r.digest, Mono: true})
	}
	st.Scans = []model.Scan{m.trivyScan(r), m.dockleScan(r)}
	return st
}

func (m *Mock) trivyScan(r *mockRun) model.Scan {
	sc := model.Scan{Tool: "Trivy", Status: model.StatusPending, Summary: "en attente"}
	done := r.cur > stepTrivy || (r.failed && r.failAt == stepTrivy)
	if !done {
		if !r.finished && r.cur == stepTrivy {
			sc.Status, sc.Summary = model.StatusRunning, "analyse en cours…"
		}
		return sc
	}
	sc.Counts = []model.Count{
		{Label: "CRITICAL", Value: r.crit, Level: "critical"},
		{Label: "HIGH", Value: r.high, Level: "high"},
		{Label: "MEDIUM", Value: r.med, Level: "medium"},
		{Label: "LOW", Value: r.low, Level: "low"},
	}
	sc.Findings = r.findings
	if r.crit+r.high > 0 {
		sc.Status, sc.Summary = model.StatusFailed, "échec : vulnérabilité HIGH/CRITICAL (--exit-code 1)"
	} else {
		sc.Status, sc.Summary = model.StatusSuccess, "aucune vulnérabilité HIGH/CRITICAL"
	}
	return sc
}

func (m *Mock) dockleScan(r *mockRun) model.Scan {
	sc := model.Scan{Tool: "Dockle", Status: model.StatusPending, Summary: "en attente"}
	switch {
	case r.failed && r.failAt <= stepDockle:
		sc.Summary = "non exécuté (scan Trivy bloquant)"
	case r.cur > stepDockle:
		sc.Status, sc.Summary = model.StatusSuccess, "PASS — CIS Docker Benchmark"
		sc.Counts = []model.Count{
			{Label: "FATAL", Value: 0, Level: "critical"},
			{Label: "WARN", Value: 0, Level: "medium"},
			{Label: "INFO", Value: 1, Level: "info"},
		}
		sc.Findings = []model.Finding{{ID: "CIS-DI-0005", Severity: "INFO", Target: "image", Title: "Content trust non activé (compensé par la signature cosign)"}}
	case !r.finished && r.cur == stepDockle:
		sc.Status, sc.Summary = model.StatusRunning, "lint en cours…"
	}
	return sc
}

func (m *Mock) phobosStage(now time.Time) model.Stage {
	return model.Stage{
		ID: model.StagePhobos, Label: "phobos", Status: model.StatusSuccess, UpdatedAt: now,
		Summary: "serveur perso · " + m.phobosTag,
		Fields: []model.Field{
			{Label: "Hôte", Value: "phobos (auto-hébergé)"},
			{Label: "URL", Value: m.cfg.phobosURL, Link: m.cfg.phobosURL},
			{Label: "Reverse proxy", Value: "Nginx Proxy Manager · TLS Let's Encrypt"},
			{Label: "Runtime", Value: "Podman (rootless)"},
			{Label: "Version déployée", Value: m.phobosTag},
			{Label: "Mode", Value: "déploiement statique, sans CI"},
		},
	}
}

func (m *Mock) composeStage(now time.Time) model.Stage {
	return model.Stage{
		ID: model.StageCompose, Label: "Podman compose", Status: model.StatusSuccess, UpdatedAt: now,
		Summary: "4/4 services healthy",
		Fields: []model.Field{
			{Label: "app", Value: "healthy · distroless · read_only · cap_drop ALL"},
			{Label: "postgres", Value: "healthy · réseau interne uniquement"},
			{Label: "redis", Value: "healthy · réseau interne uniquement"},
			{Label: "caddy", Value: "healthy · seul service exposé"},
			{Label: "Volume", Value: "pgdata (nommé, persistant)", Mono: true},
			{Label: "Réseaux", Value: "edge · backend (internal: true)", Mono: true},
		},
	}
}

func (m *Mock) githubStage(now time.Time) model.Stage {
	r := m.run
	repoURL := "https://github.com/" + m.cfg.repo
	return model.Stage{
		ID: model.StageGitHub, Label: "GitHub", Status: model.StatusSuccess, UpdatedAt: now,
		Summary: fmt.Sprintf("tag %s · %s", r.tag, r.commit[:7]),
		Fields: []model.Field{
			{Label: "Dépôt", Value: m.cfg.repo, Link: repoURL},
			{Label: "Dernier tag", Value: r.tag, Link: repoURL + "/releases/tag/" + r.tag},
			{Label: "Commit", Value: r.commit, Mono: true},
			{Label: "Branche", Value: "main"},
			{Label: "Poussé le", Value: r.startedAt.Format("02/01/2006 15:04:05")},
		},
	}
}

func (m *Mock) actionsStage(now time.Time) model.Stage {
	r := m.run
	st := model.Stage{ID: model.StageActions, Label: "GitHub Actions", UpdatedAt: now, Steps: r.steps}
	var total float64
	for _, s := range r.steps {
		total += s.DurationS
	}
	switch {
	case r.failed:
		st.Status = model.StatusFailed
		st.Summary = fmt.Sprintf("run #%d échoué · %s", r.number, r.steps[r.failAt].Name)
	case r.finished:
		st.Status = model.StatusSuccess
		st.Summary = fmt.Sprintf("run #%d réussi · %s", r.number, fmtDuration(total))
	default:
		st.Status = model.StatusRunning
		st.Summary = fmt.Sprintf("run #%d · %s", r.number, r.steps[r.cur].Name)
	}
	st.Fields = []model.Field{
		{Label: "Workflow", Value: ".github/workflows/ci.yml", Mono: true},
		{Label: "Run", Value: fmt.Sprintf("#%d", r.number), Link: m.runURL(r)},
		{Label: "Déclencheur", Value: "push du tag " + r.tag},
		{Label: "Auth Azure", Value: "OIDC (federated credentials), sans secret"},
		{Label: "Durée", Value: fmtDuration(total)},
	}
	return st
}

func (m *Mock) acrStage(now time.Time) model.Stage {
	r := m.run
	st := model.Stage{ID: model.StageACR, Label: "ACR", UpdatedAt: now, Status: model.StatusSuccess}
	top := m.acr[0]
	switch {
	case r.failed && r.failAt < stepPush:
		st.Summary = "inchangé · push bloqué (run #" + fmt.Sprint(r.number) + ")"
	case !r.finished && r.cur < stepPush:
		st.Status, st.Summary = model.StatusPending, "en attente du push de "+r.tag
	case !r.finished && r.cur == stepPush:
		st.Status, st.Summary = model.StatusRunning, "push de "+r.tag+"…"
	case !r.finished && r.cur == stepSign:
		st.Status, st.Summary = model.StatusRunning, "signature cosign de "+r.tag+"…"
	default:
		st.Summary = top.tag + " · signée"
	}
	sig := "non signée"
	if top.signed {
		sig = "cosign keyless (Sigstore · Fulcio + Rekor) — vérifiée"
	}
	var tags []string
	for _, e := range m.acr[:min(len(m.acr), 5)] {
		tags = append(tags, e.tag)
	}
	st.Fields = []model.Field{
		{Label: "Registre", Value: m.cfg.acr + ".azurecr.io", Mono: true},
		{Label: "Dépôt", Value: "containermaxxing", Mono: true},
		{Label: "Dernier tag", Value: top.tag},
		{Label: "Digest", Value: top.digest, Mono: true},
		{Label: "Signature", Value: sig},
		{Label: "Tags récents", Value: strings.Join(tags, " · ")},
	}
	return st
}

func (m *Mock) caasStage(now time.Time) model.Stage {
	r := m.run
	st := model.Stage{ID: model.StageCaaS, Label: "Container Apps", UpdatedAt: now, Status: model.StatusSuccess}
	switch {
	case r.failed:
		st.Summary = "inchangé · " + m.prod.Tag + " en prod"
	case !r.finished && r.cur < stepDeploy:
		st.Status, st.Summary = model.StatusPending, m.prod.Tag+" en prod · en attente"
	case !r.finished && r.cur == stepDeploy:
		st.Status, st.Summary = model.StatusRunning, "nouvelle révision "+r.tag+"…"
	default:
		st.Summary = m.prod.Tag + " · 100 % du trafic"
	}
	st.Fields = []model.Field{
		{Label: "Application", Value: m.cfg.caasApp, Mono: true},
		{Label: "Révision active", Value: m.cfg.caasApp + "--" + strings.ReplaceAll(m.prod.Tag, ".", "-"), Mono: true},
		{Label: "Image", Value: m.imageRef(m.prod.Tag) + "@" + m.prod.Digest[:19] + "…", Mono: true},
		{Label: "Trafic", Value: "100 % sur la dernière révision"},
		{Label: "Réplicas", Value: "1 – 3 (scale HTTP)"},
		{Label: "URL", Value: m.cfg.azureURL, Link: m.cfg.azureURL},
		{Label: "Base de données", Value: "Azure Database for PostgreSQL (Flexible Server)"},
		{Label: "En prod depuis", Value: m.prod.Since.Format("02/01/2006 15:04:05")},
	}
	return st
}

// --- utilitaires -------------------------------------------------------------

func (r *mockRun) stepLog(i int, failed bool) string {
	switch i {
	case stepBuild:
		return fmt.Sprintf("#12 exporting to image\n#12 writing image %s done", r.digest)
	case stepTrivy:
		log := fmt.Sprintf("containermaxxing:%s (gobinary)\nTotal: %d (CRITICAL: %d, HIGH: %d, MEDIUM: %d, LOW: %d)",
			r.tag, r.crit+r.high+r.med+r.low, r.crit, r.high, r.med, r.low)
		if failed {
			log += "\nError: Process completed with exit code 1."
		}
		return log
	case stepDockle:
		return "INFO\t- CIS-DI-0005: Enable Content trust for Docker\nPASS"
	case stepPush:
		return fmt.Sprintf("%s: digest: %s size: 1157", r.tag, r.digest)
	case stepSign:
		return "Generating ephemeral keys...\nRetrieving signed certificate...\ntlog entry created\nPushing signature to: containermaxxing"
	case stepDeploy:
		return "Revision provisioned and active, traffic 100%"
	case len(ciSteps) - 1:
		return "POST /api/deployments → 201 Created"
	}
	return ""
}

func fakeDigest(seed string) string {
	sum := sha256.Sum256([]byte("containermaxxing/" + seed))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

func fmtDuration(s float64) string {
	d := time.Duration(s) * time.Second
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}
