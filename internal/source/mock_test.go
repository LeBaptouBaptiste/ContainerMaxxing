package source

import (
	"context"
	"testing"

	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/config"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/store"
)

// Fait tourner le mock sur plusieurs runs : chaque déploiement enregistré doit
// passer la validation de l'API, et un run échoué ne doit jamais changer la prod.
func TestMockRuns(t *testing.T) {
	ctx := context.Background()
	st := store.NewMemory()
	m := NewMock(config.Config{Environment: "local", GitHubRepo: "o/r", ACRName: "a", CaaSApp: "c"}, st, "test")
	if err := m.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	seeded, _ := st.ListDeployments(ctx, 100)

	sawFailure := false
	for i := 0; i < 120; i++ {
		before := m.prod.Tag
		snap, err := m.Poll(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(snap.Stages) != 7 {
			t.Fatalf("%d étapes", len(snap.Stages))
		}
		if m.run.failed && m.prod.Tag != before {
			t.Fatal("un run échoué a modifié la prod")
		}
		sawFailure = sawFailure || m.run.failed
	}
	all, _ := st.ListDeployments(ctx, 100)
	if len(all) <= len(seeded) {
		t.Fatal("aucun déploiement enregistré par le mock")
	}
	for _, d := range all {
		full, _ := st.GetDeployment(ctx, d.ID)
		if err := full.Validate(); err != nil {
			t.Errorf("%s invalide: %v", d.Tag, err)
		}
	}
	if !sawFailure {
		t.Error("aucun run en échec simulé")
	}
	stats, _ := st.Stats(ctx)
	if stats.Failed == 0 || stats.FailureRate <= 0 || stats.AvgDurationS <= 0 {
		t.Errorf("stats incohérentes: %+v", stats)
	}
}
