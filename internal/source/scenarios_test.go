package source

import (
	"context"
	"testing"
	"time"

	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/config"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/model"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/store"
)

func newSeededMock(t *testing.T) (*Mock, *store.Memory) {
	t.Helper()
	st := store.NewMemory()
	m := NewMock(config.Config{Environment: "local", GitHubRepo: "o/r", GHCRImage: "ghcr.io/o/r", CaaSApp: "c"}, st, "test")
	clock := time.Now().Add(-time.Hour)
	m.now = func() time.Time { clock = clock.Add(time.Second); return clock }
	if err := m.Seed(context.Background()); err != nil {
		t.Fatal(err)
	}
	return m, st
}

// runToEnd fait avancer la release pas à pas jusqu'à sa fin.
func runToEnd(t *testing.T, m *Mock) model.Snapshot {
	t.Helper()
	for i := 0; i < len(phases)+1; i++ {
		if m.run.finished {
			return m.Snapshot()
		}
		if err := m.Step(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("la release ne se termine pas")
	return model.Snapshot{}
}

func TestScenarios(t *testing.T) {
	cases := []struct {
		scenario  string
		wantStage model.StageID // étape qui bloque ("" = succès)
	}{
		{ScenarioHealthy, ""},
		{ScenarioCVE, model.StageActions},
		{ScenarioDockle, model.StageImage},
		{ScenarioUnsigned, model.StageCaaS},
	}
	for _, c := range cases {
		t.Run(c.scenario, func(t *testing.T) {
			m, st := newSeededMock(t)
			m.EnableDemo(false)
			m.SetPaused(true) // le scénario ne doit avancer que via Step
			prodBefore, phobosBefore := m.prod.Tag, m.phobosTag
			before, _ := st.ListDeployments(context.Background(), 100)

			if err := m.Trigger(c.scenario); err != nil {
				t.Fatal(err)
			}
			tag := m.run.tag
			snap := runToEnd(t, m)

			if snap.Demo == nil || snap.Demo.Scenario != c.scenario || !snap.Demo.Paused {
				t.Fatalf("demo = %+v", snap.Demo)
			}
			if c.wantStage == "" {
				if snap.Run.Status != model.StatusSuccess || m.prod.Tag != tag || !m.registry[0].signed {
					t.Fatalf("release saine : run=%s prod=%s signée=%v", snap.Run.Status, m.prod.Tag, m.registry[0].signed)
				}
			} else {
				if snap.Run.Status != model.StatusFailed || snap.Run.Stage != c.wantStage {
					t.Fatalf("run = %s à l'étape %s, attendu échec à %s", snap.Run.Status, snap.Run.Stage, c.wantStage)
				}
				if m.prod.Tag != prodBefore {
					t.Fatal("la prod a changé malgré l'échec")
				}
			}
			switch c.scenario {
			case ScenarioDockle:
				if m.phobosTag != phobosBefore {
					t.Fatal("phobos a été mis à jour malgré l'échec Dockle")
				}
			case ScenarioUnsigned:
				if m.registry[0].tag != tag || m.registry[0].signed {
					t.Fatal("l'image non signée devrait être dans le registre, sans signature")
				}
			}

			after, _ := st.ListDeployments(context.Background(), 100)
			if len(after) == len(before) {
				t.Fatal("aucun déploiement enregistré")
			}
			for _, d := range after[:len(after)-len(before)] {
				full, _ := st.GetDeployment(context.Background(), d.ID)
				if err := full.Validate(); err != nil {
					t.Errorf("%s (%s) invalide: %v", d.Tag, d.Target, err)
				}
			}
		})
	}
}

func TestPauseFreezesProgress(t *testing.T) {
	m, _ := newSeededMock(t)
	if err := m.Trigger(ScenarioHealthy); err != nil {
		t.Fatal(err)
	}
	m.SetPaused(true)
	for i := 0; i < 10; i++ {
		if _, err := m.Poll(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if m.run.cur != 0 {
		t.Fatalf("la release a avancé en pause (step %d)", m.run.cur)
	}
	if err := m.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.run.cur != 1 {
		t.Fatalf("Step devrait terminer exactement un step (step %d)", m.run.cur)
	}
}

func TestTriggerRejectsUnknownScenario(t *testing.T) {
	m, _ := newSeededMock(t)
	if err := m.Trigger("rm -rf"); err != ErrUnknownScenario {
		t.Fatalf("err = %v", err)
	}
}
