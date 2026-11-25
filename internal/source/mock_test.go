package source

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/config"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/model"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/store"
)

var chainOrder = []model.StageID{
	model.StageImage, model.StagePhobos, model.StageCompose, model.StageGitHub,
	model.StageActions, model.StageACR, model.StageCaaS,
}

// Fait tourner le mock sur plusieurs releases avec une horloge simulée :
// - le focus (run.stage) parcourt la chaîne dans l'ordre, sans revenir en arrière ;
// - un run échoué ne change jamais la prod Azure ;
// - chaque déploiement enregistré passe la validation de l'API.
func TestMockRuns(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	m := NewMock(config.Config{Environment: "local", GitHubRepo: "o/r", ACRName: "a", CaaSApp: "c"}, st, "test")
	clock := time.Now().Add(-time.Hour) // 400 polls × 2 s restent dans le passé (Validate refuse le futur)
	m.now = func() time.Time { return clock }
	if err := m.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	seeded, _ := st.ListDeployments(ctx, 100)

	sawFailure, lastIdx, lastRun := false, -1, 0
	for i := 0; i < 400; i++ {
		clock = clock.Add(2 * time.Second)
		before := m.prod.Tag
		snap, err := m.Poll(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(snap.Stages) != len(chainOrder) {
			t.Fatalf("%d étapes", len(snap.Stages))
		}
		run := snap.Run
		if run.Number != lastRun {
			lastRun, lastIdx = run.Number, -1
		}
		if run.Status == model.StatusRunning {
			idx := slices.Index(chainOrder, run.Stage)
			if idx < lastIdx {
				t.Fatalf("le focus recule : %s après l'index %d", run.Stage, lastIdx)
			}
			lastIdx = idx
			if snap.Stages[idx].Status != model.StatusRunning {
				t.Fatalf("étape active %s en statut %s", run.Stage, snap.Stages[idx].Status)
			}
		}
		if run.Status == model.StatusFailed {
			sawFailure = true
			if m.prod.Tag != before {
				t.Fatal("un run échoué a modifié la prod")
			}
		}
	}
	all, _ := st.ListDeployments(ctx, 200)
	if len(all) <= len(seeded) {
		t.Fatal("aucun déploiement enregistré par le mock")
	}
	targets := map[string]bool{}
	for _, d := range all[:len(all)-len(seeded)] {
		targets[d.Target] = true
		full, _ := st.GetDeployment(ctx, d.ID)
		if err := full.Validate(); err != nil {
			t.Errorf("%s (%s) invalide: %v", d.Tag, d.Target, err)
		}
	}
	if !targets["phobos"] || !targets["azure"] {
		t.Errorf("cibles enregistrées : %v", targets)
	}
	if !sawFailure {
		t.Error("aucun run en échec simulé")
	}
}
