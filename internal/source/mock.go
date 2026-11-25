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

// Mock simule une release qui parcourt toute la chaîne, étape par étape :
// build et scans locaux → déploiement statique sur phobos → podman-compose →
// tag GitHub → GitHub Actions → GHCR → Container Apps.
// Chaque Poll fait avancer la release d'un cran. Chaque release suit un
// scénario (voir scenarios.go) qui décide si et où un contrôle de sécurité
// la bloque. En mode démo, le présentateur peut mettre en pause, avancer step
// par step et déclencher un scénario précis.
type Mock struct {
	mu      sync.Mutex
	cfg     cfg
	store   store.Store
	rng     *rand.Rand
	now     func() time.Time
	version string

	major, minor, patch int
	runCount            int
	nextRunAt           time.Time
	run                 *mockRun
	prod                model.ProdInfo
	registry            []regEntry // plus récent d'abord
	phobosTag           string
	githubTag           string

	paused   bool // progression figée (mode présentateur)
	demo     bool // contrôles de démo exposés dans le snapshot
	demoAuth bool // ces contrôles exigent un jeton
}

type cfg struct {
	env, repo, phobosURL, azureURL, image, caasApp string
}

type regEntry struct {
	tag, digest string
	signed      bool
}

// phaseDef est un sous-step de la release, rattaché à une étape de la chaîne.
type phaseDef struct {
	stage    model.StageID
	name     string
	min, max float64 // durée simulée (s)
	ticks    int     // nombre de polls passés sur ce step
}

// Chaque étape dure au moins 3 polls (~6 s à 2 s/poll) : le frontend a le
// temps de zoomer dessus et de laisser lire ses détails avant de passer à la suite.
var phases = []phaseDef{
	{model.StageImage, "Build multi-stage", 40, 80, 2},
	{model.StageImage, "Trivy (local)", 15, 30, 1},
	{model.StageImage, "Dockle (local)", 4, 8, 1},
	{model.StagePhobos, "Connexion SSH", 1, 3, 1},
	{model.StagePhobos, "git pull", 2, 5, 2},
	{model.StageCompose, "podman-compose up -d", 10, 25, 2},
	{model.StageCompose, "Healthchecks", 5, 15, 1},
	{model.StageGitHub, "git tag", 1, 2, 1},
	{model.StageGitHub, "git push --tags", 2, 5, 2},
	{model.StageActions, "Checkout", 2, 5, 1},
	{model.StageActions, "Build image", 45, 90, 2},
	{model.StageActions, "Trivy scan", 20, 40, 1},
	{model.StageActions, "Dockle lint", 5, 12, 1},
	{model.StageGHCR, "Push GHCR", 10, 25, 2},
	{model.StageGHCR, "Cosign sign", 4, 10, 1},
	{model.StageCaaS, "cosign verify", 2, 5, 1},
	{model.StageCaaS, "Deploy Container Apps", 30, 70, 2},
	{model.StageCaaS, "Record deployment", 1, 3, 1},
}

func phaseIndex(name string) int {
	for i, p := range phases {
		if p.name == name {
			return i
		}
	}
	panic("phase inconnue: " + name)
}

var (
	pLocalTrivy  = phaseIndex("Trivy (local)")
	pLocalDockle = phaseIndex("Dockle (local)")
	pHealth      = phaseIndex("Healthchecks")
	pPushTag     = phaseIndex("git push --tags")
	pCIFirst     = phaseIndex("Checkout")
	pCITrivy     = phaseIndex("Trivy scan")
	pCIDockle    = phaseIndex("Dockle lint")
	pPush        = phaseIndex("Push GHCR")
	pSign        = phaseIndex("Cosign sign")
	pVerify      = phaseIndex("cosign verify")
	pDeploy      = phaseIndex("Deploy Container Apps")
)

const (
	pauseAfterSuccess = 30 * time.Second // laisse le temps au tour automatique des étapes
	pauseAfterFailure = 20 * time.Second // garde le focus sur l'échec, le temps de l'expliquer
)

type mockRun struct {
	number      int
	scenario    string
	tag, digest string
	commit      string
	startedAt   time.Time
	steps       []model.Step // un par phase
	durations   []float64    // durées simulées, révélées à la fin de chaque step
	cur         int          // phase en cours ; len(phases) quand terminé
	tick        int          // polls déjà passés sur la phase en cours
	failAt      int          // -1 si la release réussit
	med, low    int
	ciCrit      int // CVE CRITICAL trouvées par le scan de la CI
	findings    []model.Finding
	finished    bool
	failed      bool
}

func NewMock(c config.Config, st store.Store, version string) *Mock {
	return &Mock{
		cfg: cfg{
			env: c.Environment, repo: c.GitHubRepo, phobosURL: c.PhobosURL,
			azureURL: c.AzureURL, image: c.GHCRImage, caasApp: c.CaaSApp,
		},
		store:   st,
		rng:     rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0x5eed)),
		now:     time.Now,
		version: version,
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
	var lastAt time.Time
	for i, h := range history {
		at := start.Add(time.Duration(i)*44*time.Hour + time.Duration(m.rng.IntN(300))*time.Minute)
		d := model.Deployment{
			Tag: h.tag, Digest: fakeDigest(h.tag), Target: h.target, StartedAt: at,
			CVECritical: h.crit, CVEHigh: h.high, CVEMedium: h.med, DockleStatus: h.dockle,
			Status: model.StatusSuccess,
		}
		first, last, failIdx := 0, pHealth, -1
		if h.target == "azure" {
			first, last = pCIFirst, len(phases)-1
			d.RunURL = fmt.Sprintf("https://github.com/%s/actions/runs/%d", m.cfg.repo, 11800000+i*137)
			if h.failAtTrivy {
				failIdx = pCITrivy
			}
		}
		t := at
		for j := first; j <= last; j++ {
			st, dur := model.StatusSuccess, m.duration(j)
			if failIdx >= 0 && j > failIdx {
				st, dur = model.StatusPending, 0
			} else if j == failIdx {
				st = model.StatusFailed
			}
			d.Steps = append(d.Steps, model.DeploymentStep{Name: phases[j].name, Status: st, StartedAt: t, DurationS: dur})
			t = t.Add(seconds(dur))
			d.DurationS += dur
		}
		d.DurationS = round1(d.DurationS)
		if failIdx >= 0 {
			d.Status, d.DockleStatus = model.StatusFailed, "skipped"
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
		if h.target == "azure" && failIdx < 0 {
			m.prod = model.ProdInfo{Tag: h.tag, Digest: d.Digest, Since: t}
			m.registry = append([]regEntry{{tag: h.tag, digest: d.Digest, signed: true}}, m.registry...)
		}
		lastAt = at
	}
	m.major, m.minor, m.patch = 1, 3, 2
	m.runCount = len(history)
	m.githubTag = m.prod.Tag
	if len(existing) > 0 {
		// Base persistante déjà remplie (redémarrage, podman-compose down/up) :
		// on reprend là où l'historique s'est arrêté au lieu de rejouer les mêmes tags.
		if err := m.restore(ctx); err != nil {
			return err
		}
	}

	// La dernière release réussie est affichée comme terminée au démarrage.
	m.run = m.newRun(m.prod.Tag, lastAt, ScenarioHealthy)
	for i := range m.run.steps {
		m.run.steps[i].Status = model.StatusSuccess
		m.run.steps[i].DurationS = m.run.durations[i]
	}
	m.run.cur, m.run.finished = len(phases), true
	m.nextRunAt = m.now().Add(4 * time.Second)
	return nil
}

// restore reconstitue l'état courant à partir des déploiements déjà enregistrés.
func (m *Mock) restore(ctx context.Context) error {
	recent, err := m.store.ListDeployments(ctx, 200) // plus récent d'abord
	if err != nil {
		return err
	}
	var prodFound, phobosFound bool
	var registry []regEntry
	for _, d := range recent {
		var a, b, c int
		if _, err := fmt.Sscanf(d.Tag, "v%d.%d.%d", &a, &b, &c); err == nil && semverLess(m.major, m.minor, m.patch, a, b, c) {
			m.major, m.minor, m.patch = a, b, c
			m.githubTag = d.Tag
		}
		if d.Status != model.StatusSuccess {
			continue
		}
		switch d.Target {
		case "azure":
			if !prodFound {
				m.prod = model.ProdInfo{Tag: d.Tag, Digest: d.Digest, Since: d.StartedAt.Add(seconds(d.DurationS))}
				prodFound = true
			}
			if len(registry) < 8 {
				registry = append(registry, regEntry{tag: d.Tag, digest: d.Digest, signed: d.Signed})
			}
		case "phobos":
			if !phobosFound {
				m.phobosTag, phobosFound = d.Tag, true
			}
		}
	}
	if len(registry) > 0 {
		m.registry = registry
	}
	m.runCount = max(m.runCount, len(recent))
	return nil
}

func semverLess(a1, b1, c1, a2, b2, c2 int) bool {
	if a1 != a2 {
		return a1 < a2
	}
	if b1 != b2 {
		return b1 < b2
	}
	return c1 < c2
}

func (m *Mock) Poll(ctx context.Context) (model.Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	err := m.advance(ctx)
	return m.snapshot(), err
}

// advance fait progresser la release d'un poll (sauf en pause).
func (m *Mock) advance(ctx context.Context) error {
	if m.paused {
		return nil
	}
	if m.run.finished {
		if now := m.now(); !now.Before(m.nextRunAt) {
			m.startRun(now, m.autoScenario())
		}
		return nil
	}
	return m.advanceRun(ctx)
}

// advanceRun compte un poll sur le step en cours et le termine quand il a duré
// assez longtemps (certains steps, comme les builds, durent plusieurs polls).
func (m *Mock) advanceRun(ctx context.Context) error {
	now := m.now()
	r := m.run
	r.tick++
	if r.tick < phases[r.cur].ticks {
		return nil
	}
	s := &r.steps[r.cur]
	s.DurationS = r.durations[r.cur]
	if r.cur == r.failAt {
		s.Status = model.StatusFailed
		s.Log = r.stepLog(r.cur, true, m.cfg.image)
		r.finished, r.failed = true, true
		m.nextRunAt = now.Add(pauseAfterFailure)
		if r.failAt <= pHealth {
			return m.recordPhobos(ctx, r)
		}
		return m.recordAzure(ctx, r)
	}
	s.Status = model.StatusSuccess
	s.Log = r.stepLog(r.cur, false, m.cfg.image)

	var err error
	switch r.cur {
	case pHealth:
		m.phobosTag = r.tag
		err = m.recordPhobos(ctx, r)
	case pPushTag:
		m.githubTag = r.tag
	case pPush:
		m.registry = append([]regEntry{{tag: r.tag, digest: r.digest}}, m.registry...)
		m.registry = m.registry[:min(len(m.registry), 8)]
	case pSign:
		if r.scenario == ScenarioUnsigned {
			// Le step de signature a été désactivé : l'image part non signée.
			s.Status, s.DurationS = model.StatusSkipped, 0
		} else {
			m.registry[0].signed = true
		}
	case pDeploy:
		m.prod = model.ProdInfo{Tag: r.tag, Digest: r.digest, Since: now}
	}

	r.cur++
	r.tick = 0
	if r.cur == len(phases) {
		r.finished = true
		m.nextRunAt = now.Add(pauseAfterSuccess)
		if e := m.recordAzure(ctx, r); e != nil {
			err = e
		}
		return err
	}
	r.steps[r.cur].Status = model.StatusRunning
	r.steps[r.cur].StartedAt = &now
	return err
}

// startRun démarre une nouvelle release (en abandonnant celle en cours).
func (m *Mock) startRun(now time.Time, scenario string) {
	m.runCount++
	if m.runCount%3 == 0 {
		m.minor, m.patch = m.minor+1, 0
	} else {
		m.patch++
	}
	tag := fmt.Sprintf("v%d.%d.%d", m.major, m.minor, m.patch)
	m.run = m.newRun(tag, now, scenario)
	m.run.steps[0].Status = model.StatusRunning
	m.run.steps[0].StartedAt = &now
}

func (m *Mock) newRun(tag string, at time.Time, scenario string) *mockRun {
	r := &mockRun{
		number: m.runCount + 30, scenario: scenario,
		tag: tag, digest: fakeDigest(tag), commit: fakeDigest("commit" + tag)[7:47],
		startedAt: at, failAt: failPhase(scenario), med: m.rng.IntN(3), low: 2 + m.rng.IntN(6),
	}
	if scenario == ScenarioCVE {
		r.ciCrit = 1
		r.findings = []model.Finding{{
			ID: "MOCK-CVE-0001", Severity: "CRITICAL", Target: "golang.org/x/net (gobinary)",
			Title: "Vulnérabilité fictive publiée après le scan local : la CI la détecte et bloque",
		}}
	}
	for i := 0; i < r.med; i++ {
		r.findings = append(r.findings, model.Finding{
			ID: fmt.Sprintf("MOCK-CVE-01%02d", i+1), Severity: "MEDIUM", Target: "stdlib (gobinary)",
			Title: "Vulnérabilité fictive de sévérité moyenne (non bloquante)",
		})
	}
	for i, p := range phases {
		r.steps = append(r.steps, model.Step{Name: p.name, Status: model.StatusPending})
		r.durations = append(r.durations, m.duration(i))
	}
	return r
}

func (m *Mock) duration(phase int) float64 {
	p := phases[phase]
	return round1(p.min + m.rng.Float64()*(p.max-p.min))
}

func (m *Mock) recordPhobos(ctx context.Context, r *mockRun) error {
	d := model.Deployment{
		Tag: r.tag, Digest: r.digest, Target: "phobos", Status: model.StatusSuccess,
		CVEMedium: r.med, DockleStatus: "pass", StartedAt: r.startedAt,
	}
	if r.failed {
		d.Status = model.StatusFailed
		if r.failAt == pLocalDockle {
			d.DockleStatus = "warn"
		} else if r.failAt < pLocalDockle {
			d.DockleStatus = "skipped"
		}
	}
	d.Steps, d.DurationS = r.deploymentSteps(0, pHealth)
	_, err := m.store.CreateDeployment(ctx, d)
	return err
}

func (m *Mock) recordAzure(ctx context.Context, r *mockRun) error {
	d := model.Deployment{
		Tag: r.tag, Digest: r.digest, Target: "azure", Status: model.StatusSuccess,
		CVECritical: r.ciCrit, CVEMedium: r.med, DockleStatus: "pass",
		Signed: r.steps[pSign].Status == model.StatusSuccess, RunURL: m.runURL(r),
	}
	if r.failed {
		d.Status = model.StatusFailed
		if r.failAt <= pCIDockle {
			d.DockleStatus = "skipped"
		}
	}
	d.Steps, d.DurationS = r.deploymentSteps(pCIFirst, len(phases)-1)
	d.StartedAt = d.Steps[0].StartedAt
	_, err := m.store.CreateDeployment(ctx, d)
	return err
}

func (r *mockRun) deploymentSteps(from, to int) ([]model.DeploymentStep, float64) {
	var out []model.DeploymentStep
	var total float64
	t := r.startedAt
	for i := from; i <= to; i++ {
		s := r.steps[i]
		if s.StartedAt != nil {
			t = *s.StartedAt
		}
		out = append(out, model.DeploymentStep{Name: s.Name, Status: s.Status, StartedAt: t, DurationS: s.DurationS})
		t = t.Add(seconds(s.DurationS))
		total += s.DurationS
	}
	return out, round1(total)
}

func (m *Mock) runURL(r *mockRun) string {
	return fmt.Sprintf("https://github.com/%s/actions/runs/%d", m.cfg.repo, 11801500+r.number)
}

// --- construction du snapshot ---------------------------------------------

func (m *Mock) snapshot() model.Snapshot {
	now := m.now()
	r := m.run
	run := &model.RunInfo{
		Number: r.number, Tag: r.tag, StepCount: len(phases), StartedAt: r.startedAt,
		StepsDone: min(r.cur, len(phases)),
	}
	switch {
	case r.failed:
		run.Status, run.Stage, run.StepName = model.StatusFailed, phases[r.failAt].stage, phases[r.failAt].name
	case r.finished:
		run.Status = model.StatusSuccess
	default:
		run.Status, run.Stage, run.StepName = model.StatusRunning, phases[r.cur].stage, phases[r.cur].name
	}
	snap := model.Snapshot{
		Environment: m.cfg.env,
		Source:      "mock",
		AppVersion:  m.version,
		Prod:        m.prod,
		Run:         run,
		GeneratedAt: now,
		Stages: []model.Stage{
			m.imageStage(now), m.phobosStage(now), m.composeStage(now),
			m.githubStage(now), m.actionsStage(now), m.ghcrStage(now), m.caasStage(now),
		},
	}
	if m.demo {
		snap.Demo = &model.DemoInfo{
			AuthRequired: m.demoAuth, Paused: m.paused, Scenario: r.scenario, Scenarios: scenarioList,
		}
	}
	return snap
}

// stageSteps renvoie les steps de l'étape et le statut qui en découle.
// Le workflow CI est découpé en jobs enchaînés (needs:) : build-scan → étape
// Actions, publish → étape GHCR, deploy → étape Container Apps. Une étape est
// donc terminée avant que la suivante démarre.
func (m *Mock) stageSteps(id model.StageID) ([]model.Step, model.Status) {
	r := m.run
	var steps []model.Step
	for i, p := range phases {
		if p.stage == id {
			steps = append(steps, r.steps[i])
		}
	}
	var done, failed, running int
	for _, s := range steps {
		switch s.Status {
		case model.StatusSuccess, model.StatusSkipped:
			done++
		case model.StatusFailed:
			failed++
		case model.StatusRunning:
			running++
		}
	}
	switch {
	case failed > 0:
		return steps, model.StatusFailed
	case running > 0:
		return steps, model.StatusRunning
	case done == len(steps):
		return steps, model.StatusSuccess
	case done > 0 && !r.finished:
		return steps, model.StatusRunning
	default:
		return steps, model.StatusPending
	}
}

func (r *mockRun) current() string {
	if r.cur < len(phases) {
		return r.steps[r.cur].Name
	}
	return ""
}

func (r *mockRun) blockedBy() string {
	if r.failed {
		return phases[r.failAt].name
	}
	return ""
}

func (m *Mock) imageRef(tag string) string {
	return m.cfg.image + ":" + tag
}

// packageURL pointe vers la page du package GHCR sur GitHub.
func (m *Mock) packageURL() string {
	owner, name, _ := strings.Cut(strings.TrimPrefix(m.cfg.image, "ghcr.io/"), "/")
	return fmt.Sprintf("https://github.com/users/%s/packages/container/package/%s", owner, name)
}

func (m *Mock) imageStage(now time.Time) model.Stage {
	r := m.run
	steps, status := m.stageSteps(model.StageImage)
	st := model.Stage{ID: model.StageImage, Label: "Image", UpdatedAt: now, Status: status, Steps: steps}
	switch status {
	case model.StatusRunning:
		st.Summary = r.current() + " · " + r.tag
	case model.StatusFailed:
		st.Summary = r.tag + " bloquée par " + r.blockedBy()
	default:
		st.Summary = r.tag + " · distroless · scannée"
	}
	user := "65532 (nonroot)"
	if r.scenario == ScenarioDockle {
		user = "0 (root) — USER absent du Dockerfile"
	}
	st.Fields = []model.Field{
		{Label: "Image", Value: "containermaxxing:" + r.tag, Mono: true},
		{Label: "Image de base", Value: "gcr.io/distroless/static:nonroot", Mono: true},
		{Label: "Utilisateur", Value: user},
		{Label: "Shell", Value: "aucun (distroless)"},
		{Label: "Compilation", Value: "CGO_ENABLED=0 · -trimpath · -ldflags='-s -w'", Mono: true},
		{Label: "Taille", Value: "≈ 9,6 Mo"},
	}
	if r.steps[0].Status == model.StatusSuccess {
		st.Fields = append(st.Fields, model.Field{Label: "Digest", Value: r.digest, Mono: true})
	}
	st.Scans = []model.Scan{m.trivyScan(r, pLocalTrivy, 0), m.dockleScan(r, pLocalDockle)}
	return st
}

func (m *Mock) phobosStage(now time.Time) model.Stage {
	r := m.run
	steps, status := m.stageSteps(model.StagePhobos)
	st := model.Stage{ID: model.StagePhobos, Label: "phobos", Status: status, UpdatedAt: now, Steps: steps}
	switch {
	case status == model.StatusRunning:
		st.Summary = r.current() + " · " + r.tag
	case status == model.StatusPending && r.failed:
		st.Summary = "inchangé · sert " + m.phobosTag
	case status == model.StatusPending:
		st.Summary = "sert " + m.phobosTag + " · en attente"
	default:
		st.Summary = "serveur perso · " + m.phobosTag
	}
	st.Fields = []model.Field{
		{Label: "Hôte", Value: "phobos (auto-hébergé)"},
		{Label: "URL", Value: m.cfg.phobosURL, Link: m.cfg.phobosURL},
		{Label: "Reverse proxy", Value: "Nginx Proxy Manager sur beacon · TLS Let's Encrypt"},
		{Label: "Runtime", Value: "Podman (rootless)"},
		{Label: "Version déployée", Value: m.phobosTag},
		{Label: "Mode", Value: "déploiement statique, sans CI"},
	}
	return st
}

func (m *Mock) composeStage(now time.Time) model.Stage {
	r := m.run
	steps, status := m.stageSteps(model.StageCompose)
	st := model.Stage{ID: model.StageCompose, Label: "Podman compose", Status: status, UpdatedAt: now, Steps: steps}
	health := "healthy"
	switch {
	case status == model.StatusRunning:
		st.Summary = r.current() + "…"
		health = "démarrage"
	case status == model.StatusPending && r.failed:
		st.Summary = "inchangé · 3/3 services healthy"
	case status == model.StatusPending:
		st.Summary = "3/3 services healthy · en attente"
	default:
		st.Summary = "3/3 services healthy"
	}
	st.Fields = []model.Field{
		{Label: "app", Value: health + " · distroless · read_only · cap_drop ALL · seul service exposé (vers NPM)"},
		{Label: "postgres", Value: health + " · réseau interne uniquement"},
		{Label: "redis", Value: health + " · réseau interne uniquement"},
		{Label: "Volume", Value: "pgdata (nommé, persistant)", Mono: true},
		{Label: "Réseaux", Value: "edge (app) · backend (internal: true)", Mono: true},
	}
	return st
}

func (m *Mock) githubStage(now time.Time) model.Stage {
	r := m.run
	steps, status := m.stageSteps(model.StageGitHub)
	st := model.Stage{ID: model.StageGitHub, Label: "GitHub", Status: status, UpdatedAt: now, Steps: steps}
	repoURL := "https://github.com/" + m.cfg.repo
	switch {
	case status == model.StatusRunning:
		st.Summary = r.current() + " " + r.tag
	case status == model.StatusPending && r.failed:
		st.Summary = "inchangé · dernier tag " + m.githubTag
	case status == model.StatusPending:
		st.Summary = "dernier tag " + m.githubTag + " · en attente"
	default:
		st.Summary = fmt.Sprintf("tag %s · %s", m.githubTag, r.commit[:7])
	}
	st.Fields = []model.Field{
		{Label: "Dépôt", Value: m.cfg.repo, Link: repoURL},
		{Label: "Dernier tag", Value: m.githubTag, Link: repoURL + "/releases/tag/" + m.githubTag},
		{Label: "Commit", Value: r.commit, Mono: true},
		{Label: "Branche", Value: "main"},
		{Label: "Déclenche", Value: "workflow ci.yml (on: push tags v*)"},
	}
	return st
}

func (m *Mock) actionsStage(now time.Time) model.Stage {
	r := m.run
	steps, status := m.stageSteps(model.StageActions)
	st := model.Stage{ID: model.StageActions, Label: "GitHub Actions", UpdatedAt: now, Status: status, Steps: steps}
	var total float64
	for _, s := range steps {
		total += s.DurationS
	}
	switch {
	case status == model.StatusFailed:
		st.Summary = fmt.Sprintf("run #%d échoué · %s", r.number, r.blockedBy())
	case status == model.StatusRunning:
		st.Summary = fmt.Sprintf("run #%d · %s", r.number, r.current())
	case status == model.StatusPending && r.failed:
		st.Summary = "non déclenché"
	case status == model.StatusPending:
		st.Summary = "en attente du tag " + r.tag
	default:
		st.Summary = fmt.Sprintf("run #%d · build et scans OK", r.number)
	}
	st.Fields = []model.Field{
		{Label: "Workflow", Value: ".github/workflows/ci.yml", Mono: true},
		{Label: "Run", Value: fmt.Sprintf("#%d", r.number), Link: m.runURL(r)},
		{Label: "Déclencheur", Value: "push du tag " + r.tag},
		{Label: "Jobs", Value: "build-scan → publish (GHCR) → deploy (Container Apps)"},
		{Label: "Durée du job", Value: fmtDuration(total)},
	}
	st.Scans = []model.Scan{m.trivyScan(r, pCITrivy, r.ciCrit), m.dockleScan(r, pCIDockle)}
	return st
}

func (m *Mock) ghcrStage(now time.Time) model.Stage {
	r := m.run
	steps, status := m.stageSteps(model.StageGHCR)
	st := model.Stage{ID: model.StageGHCR, Label: "GHCR", UpdatedAt: now, Status: status, Steps: steps}
	top := m.registry[0]
	switch {
	case status == model.StatusRunning:
		st.Summary = r.current() + " · " + r.tag
	case r.failed && status == model.StatusPending:
		st.Summary = "inchangé · push bloqué en amont"
	case status == model.StatusPending:
		st.Summary = "dernier tag " + top.tag + " · en attente"
	case !top.signed:
		st.Summary = top.tag + " · NON signée"
	default:
		st.Summary = top.tag + " · signée"
	}
	sig := "non signée (step cosign sign ignoré)"
	if top.signed {
		sig = "cosign keyless (Sigstore · Fulcio + Rekor)"
	}
	var tags []string
	for _, e := range m.registry[:min(len(m.registry), 5)] {
		tags = append(tags, e.tag)
	}
	st.Fields = []model.Field{
		{Label: "Package", Value: m.cfg.image, Mono: true, Link: m.packageURL()},
		{Label: "Dernier tag", Value: top.tag},
		{Label: "Digest", Value: top.digest, Mono: true},
		{Label: "Signature", Value: sig},
		{Label: "Push", Value: "GITHUB_TOKEN éphémère du run (permissions: packages: write)"},
		{Label: "Tags récents", Value: strings.Join(tags, " · ")},
	}
	return st
}

func (m *Mock) caasStage(now time.Time) model.Stage {
	r := m.run
	steps, status := m.stageSteps(model.StageCaaS)
	st := model.Stage{ID: model.StageCaaS, Label: "Container Apps", UpdatedAt: now, Status: status, Steps: steps}
	switch {
	case status == model.StatusRunning:
		st.Summary = r.current() + " · " + r.tag
	case status == model.StatusFailed:
		st.Summary = "déploiement refusé · " + m.prod.Tag + " reste en prod"
	case r.failed && status == model.StatusPending:
		st.Summary = "inchangé · " + m.prod.Tag + " reste en prod"
	case status == model.StatusPending:
		st.Summary = m.prod.Tag + " en prod · en attente"
	default:
		st.Summary = m.prod.Tag + " · 100 % du trafic"
	}
	st.Fields = []model.Field{
		{Label: "Application", Value: m.cfg.caasApp, Mono: true},
		{Label: "Révision active", Value: m.cfg.caasApp + "--" + strings.ReplaceAll(m.prod.Tag, ".", "-"), Mono: true},
		{Label: "Image", Value: m.imageRef(m.prod.Tag) + "@" + m.prod.Digest[:19] + "…", Mono: true},
		{Label: "Politique", Value: "signature cosign vérifiée avant chaque déploiement"},
		{Label: "Trafic", Value: "100 % sur la dernière révision"},
		{Label: "Réplicas", Value: "1 – 3 (scale HTTP)"},
		{Label: "URL", Value: m.cfg.azureURL, Link: m.cfg.azureURL},
		{Label: "Auth Azure", Value: "OIDC (federated credentials), sans secret"},
		{Label: "Base de données", Value: "Azure Database for PostgreSQL (Flexible Server)"},
		{Label: "En prod depuis", Value: m.prod.Since.Format("02/01/2006 15:04:05")},
	}
	return st
}

func (m *Mock) trivyScan(r *mockRun, phase, crit int) model.Scan {
	sc := model.Scan{Tool: "Trivy", Status: model.StatusPending, Summary: "en attente"}
	switch r.steps[phase].Status {
	case model.StatusRunning:
		sc.Status, sc.Summary = model.StatusRunning, "analyse en cours…"
		return sc
	case model.StatusPending:
		if r.failed && r.failAt < phase {
			sc.Summary = "non exécuté (release bloquée plus tôt)"
		}
		return sc
	}
	sc.Counts = []model.Count{
		{Label: "CRITICAL", Value: crit, Level: "critical"},
		{Label: "HIGH", Value: 0, Level: "high"},
		{Label: "MEDIUM", Value: r.med, Level: "medium"},
		{Label: "LOW", Value: r.low, Level: "low"},
	}
	for _, f := range r.findings {
		if f.Severity != "CRITICAL" || crit > 0 {
			sc.Findings = append(sc.Findings, f)
		}
	}
	if crit > 0 {
		sc.Status, sc.Summary = model.StatusFailed, "échec : vulnérabilité HIGH/CRITICAL (--exit-code 1)"
	} else {
		sc.Status, sc.Summary = model.StatusSuccess, "aucune vulnérabilité HIGH/CRITICAL"
	}
	return sc
}

func (m *Mock) dockleScan(r *mockRun, phase int) model.Scan {
	sc := model.Scan{Tool: "Dockle", Status: model.StatusPending, Summary: "en attente"}
	switch r.steps[phase].Status {
	case model.StatusRunning:
		sc.Status, sc.Summary = model.StatusRunning, "lint en cours…"
	case model.StatusFailed:
		sc.Status, sc.Summary = model.StatusFailed, "échec : CIS-DI-0001 (WARN, --exit-level warn)"
		sc.Counts = []model.Count{
			{Label: "FATAL", Value: 0, Level: "critical"},
			{Label: "WARN", Value: 1, Level: "medium"},
			{Label: "INFO", Value: 1, Level: "info"},
		}
		sc.Findings = []model.Finding{
			{ID: "CIS-DI-0001", Severity: "WARN", Target: "image", Title: "Le conteneur tourne en root : aucun USER défini"},
			{ID: "CIS-DI-0005", Severity: "INFO", Target: "image", Title: "Content trust non activé (compensé par la signature cosign)"},
		}
	case model.StatusSuccess:
		sc.Status, sc.Summary = model.StatusSuccess, "PASS — CIS Docker Benchmark"
		sc.Counts = []model.Count{
			{Label: "FATAL", Value: 0, Level: "critical"},
			{Label: "WARN", Value: 0, Level: "medium"},
			{Label: "INFO", Value: 1, Level: "info"},
		}
		sc.Findings = []model.Finding{{ID: "CIS-DI-0005", Severity: "INFO", Target: "image", Title: "Content trust non activé (compensé par la signature cosign)"}}
	default:
		if r.failed && r.failAt < phase {
			sc.Summary = "non exécuté (release bloquée plus tôt)"
		}
	}
	return sc
}

// --- utilitaires -------------------------------------------------------------

func (r *mockRun) stepLog(i int, failed bool, image string) string {
	switch phases[i].name {
	case "Build multi-stage", "Build image":
		return fmt.Sprintf("[builder] go build -trimpath -ldflags='-s -w' ./cmd/server\n[final] FROM gcr.io/distroless/static:nonroot\nwriting image %s done", r.digest)
	case "Trivy (local)":
		return fmt.Sprintf("containermaxxing:%s (gobinary)\nTotal: %d (CRITICAL: 0, HIGH: 0, MEDIUM: %d, LOW: %d)", r.tag, r.med+r.low, r.med, r.low)
	case "Trivy scan":
		log := fmt.Sprintf("containermaxxing:%s (gobinary)\nTotal: %d (CRITICAL: %d, HIGH: 0, MEDIUM: %d, LOW: %d)", r.tag, r.ciCrit+r.med+r.low, r.ciCrit, r.med, r.low)
		if failed {
			log += "\nMOCK-CVE-0001  CRITICAL  golang.org/x/net\nError: Process completed with exit code 1."
		}
		return log
	case "Dockle (local)", "Dockle lint":
		if failed {
			return "WARN\t- CIS-DI-0001: Create a user for the container\n\t* Last user should not be root\nINFO\t- CIS-DI-0005: Enable Content trust for Docker\nError: exit status 1 (--exit-level warn)"
		}
		return "INFO\t- CIS-DI-0005: Enable Content trust for Docker\nPASS"
	case "Connexion SSH":
		return "ssh deploy@phobos — authentification par clé ed25519"
	case "git pull":
		return "Fast-forward main → " + r.commit[:7]
	case "podman-compose up -d":
		return "podman-compose up -d\n✔ postgres  ✔ redis  ✔ app"
	case "Healthchecks":
		return "app: healthy\npostgres: healthy\nredis: healthy"
	case "git tag":
		return "git tag -s " + r.tag + " -m \"release " + r.tag + "\""
	case "git push --tags":
		return "* [new tag] " + r.tag + " -> " + r.tag
	case "Checkout":
		return "actions/checkout@v4 — " + r.commit[:7]
	case "Push GHCR":
		return fmt.Sprintf("The push refers to repository [%s]\n%s: digest: %s size: 1157", image, r.tag, r.digest)
	case "Cosign sign":
		if r.scenario == ScenarioUnsigned {
			return "step ignoré : la signature a été désactivée dans le workflow"
		}
		return "Generating ephemeral keys...\nRetrieving signed certificate...\ntlog entry created\nPushing signature to: " + image
	case "cosign verify":
		if failed {
			return fmt.Sprintf("cosign verify %s:%s \\\n  --certificate-identity-regexp '.github/workflows/ci.yml@refs/tags/v.*'\nError: no matching signatures\n→ déploiement refusé : image non signée par le workflow de CI", image, r.tag)
		}
		return fmt.Sprintf("Verification for %s:%s --\n  ✓ certificat émis pour .github/workflows/ci.yml@refs/tags/%s\n  ✓ entrée présente dans le journal de transparence Rekor", image, r.tag, r.tag)
	case "Deploy Container Apps":
		return "Revision provisioned and active, traffic 100%"
	case "Record deployment":
		return "POST /api/deployments → 201 Created"
	}
	return ""
}

func fakeDigest(seed string) string {
	sum := sha256.Sum256([]byte("containermaxxing/" + seed))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

func fmtDuration(s float64) string {
	d := time.Duration(s) * time.Second
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}
