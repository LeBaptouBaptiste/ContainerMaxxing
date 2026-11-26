package source

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/config"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/github"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/model"
)

type fakeAPI struct {
	commit    github.Commit
	commitErr error
	tags      []github.Tag
	runs      []github.Run
	runsErr   error
	recent    []github.Run
	jobs      []github.Job
	jobLog    string
}

func (f *fakeAPI) Tags(context.Context) ([]github.Tag, error) { return f.tags, nil }
func (f *fakeAPI) LatestCommit(context.Context, string) (github.Commit, error) {
	return f.commit, f.commitErr
}
func (f *fakeAPI) WorkflowRuns(context.Context, string, int) ([]github.Run, error) {
	return f.runs, f.runsErr
}
func (f *fakeAPI) RecentRuns(context.Context, int) ([]github.Run, error) { return f.recent, nil }
func (f *fakeAPI) Jobs(context.Context, int64) ([]github.Job, error)     { return f.jobs, nil }
func (f *fakeAPI) JobLogs(context.Context, int64) (string, error)        { return f.jobLog, nil }
func (f *fakeAPI) RunLogs(context.Context, int64) ([]byte, error)        { return nil, github.ErrNotFound }

func liveConfig(env string) config.Config {
	return config.Config{Environment: env, GitHubRepo: "o/r", GitHubWorkflow: "ci.yml", GitHubBranch: "main", PhobosURL: "https://p.example"}
}

func someCommit() github.Commit {
	var c github.Commit
	c.SHA = "0123456789abcdef0123456789abcdef01234567"
	c.HTMLURL = "https://github.com/o/r/commit/0123456"
	c.Commit.Message = "Ajoute une fonctionnalité\n\nCorps du message"
	return c
}

func stageByID(t *testing.T, s model.Snapshot, id model.StageID) model.Stage {
	t.Helper()
	for _, st := range s.Stages {
		if st.ID == id {
			return st
		}
	}
	t.Fatalf("étape %s absente", id)
	return model.Stage{}
}

func ts(sec int) *time.Time {
	t := time.Date(2026, 11, 25, 10, 0, sec, 0, time.UTC)
	return &t
}

func step(name, status, conclusion string) github.Step {
	return github.Step{Name: name, Status: status, Conclusion: conclusion, StartedAt: ts(0), CompletedAt: ts(30)}
}

// Pas encore de ci.yml (aujourd'hui) : Actions « inconnu », mais les derniers runs du dépôt sont listés.
func TestGitHubSourceWithoutCI(t *testing.T) {
	api := &fakeAPI{
		commit:  someCommit(),
		runsErr: github.ErrNotFound,
		recent:  []github.Run{{RunNumber: 13, Name: "Dépendances entre PR", Status: "completed", Conclusion: "success", HTMLURL: "https://github.com/o/r/actions/runs/1"}},
	}
	snap, err := NewGitHub(liveConfig("local"), api, "v0").Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Source != "live" || snap.Run != nil || snap.Demo != nil || len(snap.Stages) != 7 {
		t.Fatalf("snapshot = %+v", snap)
	}
	gh := stageByID(t, snap, model.StageGitHub)
	if gh.Status != model.StatusSuccess || gh.Summary != "aucun tag · 0123456" {
		t.Fatalf("github = %s / %s", gh.Status, gh.Summary)
	}
	act := stageByID(t, snap, model.StageActions)
	if act.Status != model.StatusUnknown || len(act.Fields) != 1 || act.Fields[0].Label != "#13" {
		t.Fatalf("actions = %+v", act)
	}
	for _, id := range []model.StageID{model.StageImage, model.StagePhobos, model.StageCompose, model.StageGHCR, model.StageCaaS} {
		if st := stageByID(t, snap, id); st.Status != model.StatusUnknown {
			t.Errorf("%s = %s, attendu inconnu (non branché)", id, st.Status)
		}
	}
}

// Run de la CI en cours : les jobs sont répartis sur Actions / GHCR / Container Apps,
// et la caméra suit l'étape du job en cours.
func TestGitHubSourceRunInProgress(t *testing.T) {
	api := &fakeAPI{
		commit: someCommit(),
		tags:   []github.Tag{{Name: "v1.2.0"}, {Name: "v1.10.0"}, {Name: "latest"}, {Name: "v1.9.3"}},
		runs:   []github.Run{{ID: 9, RunNumber: 21, Status: "in_progress", HeadBranch: "v1.10.0", HeadSHA: "abcdef1234", Event: "push"}},
		jobs: []github.Job{
			{Name: "build-scan", Status: "completed", Conclusion: "success", Steps: []github.Step{
				step("Set up job", "completed", "success"),
				step("Build image", "completed", "success"),
				step("Trivy scan", "completed", "success"),
				step("Complete job", "completed", "success"),
			}},
			{Name: "publish", Status: "in_progress", Steps: []github.Step{
				step("Push GHCR", "completed", "success"),
				{Name: "Cosign sign", Status: "in_progress", StartedAt: ts(40)},
			}},
			{Name: "deploy", Status: "queued"},
		},
	}
	snap, err := NewGitHub(liveConfig("local"), api, "v0").Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if gh := stageByID(t, snap, model.StageGitHub); gh.Fields[1].Value != "v1.10.0" {
		t.Fatalf("dernier tag = %s (tri semver attendu)", gh.Fields[1].Value)
	}
	act := stageByID(t, snap, model.StageActions)
	if act.Status != model.StatusSuccess || len(act.Steps) != 2 || act.Steps[0].DurationS != 30 {
		t.Fatalf("actions = %s, steps %+v (Set up / Complete job exclus)", act.Status, act.Steps)
	}
	ghcr := stageByID(t, snap, model.StageGHCR)
	if ghcr.Status != model.StatusRunning || ghcr.Summary != "Cosign sign" {
		t.Fatalf("ghcr = %s / %s", ghcr.Status, ghcr.Summary)
	}
	if caas := stageByID(t, snap, model.StageCaaS); caas.Status != model.StatusUnknown {
		t.Fatalf("caas sans steps = %s", caas.Status)
	}
	r := snap.Run
	if r == nil || r.Number != 21 || r.Tag != "v1.10.0" || r.Status != model.StatusRunning ||
		r.Stage != model.StageGHCR || r.StepName != "Cosign sign" || r.StepsDone != 3 || r.StepCount != 4 {
		t.Fatalf("run = %+v", r)
	}
	if snap.Prod.Tag != "" {
		t.Fatalf("prod = %q, aucun run réussi pour l'instant", snap.Prod.Tag)
	}
}

// Échec au scan : la release est bloquée sur Actions ; la prod reste le dernier run réussi.
func TestGitHubSourceFailedRun(t *testing.T) {
	api := &fakeAPI{
		commit: someCommit(),
		runs: []github.Run{
			{ID: 10, RunNumber: 22, Status: "completed", Conclusion: "failure", HeadBranch: "v1.11.0"},
			{ID: 9, RunNumber: 21, Status: "completed", Conclusion: "success", HeadBranch: "v1.10.0", UpdatedAt: *ts(0)},
		},
		jobs: []github.Job{{Name: "build-scan", Status: "completed", Conclusion: "failure", Steps: []github.Step{
			step("Build image", "completed", "success"),
			step("Trivy scan", "completed", "failure"),
			step("Dockle", "completed", "skipped"),
		}}},
	}
	snap, err := NewGitHub(liveConfig("local"), api, "v0").Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Run.Status != model.StatusFailed || snap.Run.Stage != model.StageActions || snap.Run.StepName != "Trivy scan" {
		t.Fatalf("run = %+v", snap.Run)
	}
	if act := stageByID(t, snap, model.StageActions); act.Status != model.StatusFailed || act.Summary != "run #22 échoué · Trivy scan" {
		t.Fatalf("actions = %s / %s", act.Status, act.Summary)
	}
	if snap.Prod.Tag != "v1.10.0" {
		t.Fatalf("prod = %q, attendu le dernier run réussi", snap.Prod.Tag)
	}
}

// API indisponible : pas de panique, étape GitHub « inconnu », erreur renvoyée pour les logs.
func TestGitHubSourceAPIDown(t *testing.T) {
	api := &fakeAPI{commitErr: errors.New("connexion refusée"), runsErr: github.ErrRateLimited}
	snap, err := NewGitHub(liveConfig("local"), api, "v0").Poll(context.Background())
	if err == nil {
		t.Fatal("une erreur doit être remontée")
	}
	if gh := stageByID(t, snap, model.StageGitHub); gh.Status != model.StatusUnknown || gh.Summary != "API GitHub indisponible" {
		t.Fatalf("github = %s / %s", gh.Status, gh.Summary)
	}
	if len(snap.Stages) != 7 {
		t.Fatal("le snapshot doit rester complet")
	}
}

// Sur phobos, l'app sait qu'elle y est servie : seul fait « réel » côté phobos pour l'instant.
func TestGitHubSourcePhobos(t *testing.T) {
	snap, _ := NewGitHub(liveConfig("phobos"), &fakeAPI{commit: someCommit(), runsErr: github.ErrNotFound}, "v1.0.0").Poll(context.Background())
	if st := stageByID(t, snap, model.StagePhobos); st.Status != model.StatusSuccess {
		t.Fatalf("phobos = %s", st.Status)
	}
}

func TestSemverHelpers(t *testing.T) {
	for in, want := range map[string]bool{"v1.2.3": true, "v10.0.1": true, "1.2.3": false, "v1.2": false, "v1.2.3-rc1": false, "latest": false} {
		if isSemver(in) != want {
			t.Errorf("isSemver(%q) != %v", in, want)
		}
	}
	if got := truncate("ééééé", 3); got != "éé…" {
		t.Errorf("truncate = %q", got)
	}
}
