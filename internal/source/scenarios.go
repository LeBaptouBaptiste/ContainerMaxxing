package source

import (
	"context"
	"errors"
	"time"

	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/model"
)

// Scénarios de démo : chacun met en évidence un contrôle de sécurité différent,
// à une étape différente de la chaîne (défense en profondeur).
const (
	ScenarioHealthy  = "healthy"
	ScenarioCVE      = "cve"
	ScenarioDockle   = "dockle"
	ScenarioUnsigned = "unsigned"
)

var scenarioList = []model.ScenarioInfo{
	{ID: ScenarioHealthy, Label: "Release saine",
		Description: "Tous les contrôles passent : la version arrive en prod."},
	{ID: ScenarioCVE, Label: "CVE critique",
		Description: "Une CVE CRITICAL est publiée après le scan local : Trivy la détecte en CI et bloque la release."},
	{ID: ScenarioDockle, Label: "Conteneur root",
		Description: "Le Dockerfile ne définit plus d'utilisateur : Dockle bloque dès le build local."},
	{ID: ScenarioUnsigned, Label: "Image non signée",
		Description: "La signature est contournée : cosign verify refuse le déploiement sur Container Apps."},
}

// failPhase renvoie la phase où le scénario échoue (-1 : release réussie).
func failPhase(scenario string) int {
	switch scenario {
	case ScenarioCVE:
		return pCITrivy
	case ScenarioDockle:
		return pLocalDockle
	case ScenarioUnsigned:
		return pVerify
	default:
		return -1
	}
}

// ErrUnknownScenario est renvoyée pour un identifiant de scénario inconnu.
var ErrUnknownScenario = errors.New("scénario inconnu")

func validScenario(id string) bool {
	for _, s := range scenarioList {
		if s.ID == id {
			return true
		}
	}
	return false
}

// --- contrôles du mode démo / présentateur ---------------------------------------

// EnableDemo active les contrôles de démo dans le snapshot.
func (m *Mock) EnableDemo(authRequired bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.demo, m.demoAuth = true, authRequired
}

// Trigger lance immédiatement une release avec le scénario donné.
// Une release en cours est abandonnée (sans être enregistrée).
func (m *Mock) Trigger(id string) error {
	if !validScenario(id) {
		return ErrUnknownScenario
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startRun(m.now(), id)
	return nil
}

// SetPaused fige ou relance la progression automatique.
func (m *Mock) SetPaused(paused bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.paused = paused
	if !paused && m.run.finished {
		// À la reprise, on laisse le temps de voir l'état final avant la release suivante.
		m.nextRunAt = m.now().Add(5 * time.Second)
	}
}

// Step termine le step en cours (même en pause). Une release terminée n'est pas
// relancée : un appui de trop ne doit pas effacer l'écran de fin (échec ou
// succès) pendant une présentation ; on relance explicitement via Trigger.
func (m *Mock) Step(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.run
	if r.finished {
		return nil
	}
	r.tick = phases[r.cur].ticks - 1 // la prochaine avance termine le step
	return m.advanceRun(ctx)
}

// Snapshot renvoie l'état courant sans faire avancer la release.
func (m *Mock) Snapshot() model.Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshot()
}

// autoScenario : en pilote automatique, une release sur quatre échoue (CVE).
func (m *Mock) autoScenario() string {
	if (m.runCount+1)%4 == 2 {
		return ScenarioCVE
	}
	return ScenarioHealthy
}
