package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/cache"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// newClient crée un client sur un faux serveur GitHub, sans cache partagé
// (TTL quasi nul) pour que chaque appel atteigne le serveur.
func newClient(t *testing.T, h http.HandlerFunc, token string) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New(Options{
		BaseURL: srv.URL, Repo: "owner/repo", Token: token,
		Loader: cache.NewLoader(cache.NewMemory(100), discard), Log: discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestHeadersAndDecoding(t *testing.T) {
	var got http.Header
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		if r.URL.Path != "/repos/owner/repo/tags" {
			t.Errorf("chemin = %s", r.URL.Path)
		}
		fmt.Fprint(w, `[{"name":"v1.2.0","commit":{"sha":"abc"}}]`)
	}, "secret-token")
	tags, err := c.Tags(context.Background())
	if err != nil || len(tags) != 1 || tags[0].Name != "v1.2.0" {
		t.Fatalf("tags = %+v, %v", tags, err)
	}
	if got.Get("Authorization") != "Bearer secret-token" || got.Get("X-GitHub-Api-Version") != apiVersion ||
		got.Get("Accept") != "application/vnd.github+json" || got.Get("User-Agent") == "" {
		t.Fatalf("en-têtes = %v", got)
	}
}

func TestNoTokenNoAuthorizationHeader(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("en-tête Authorization envoyé sans jeton")
		}
		fmt.Fprint(w, `[]`)
	}, "")
	if _, err := c.Tags(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// Une réponse 304 réutilise le corps déjà reçu (et ne consomme pas de quota chez GitHub).
func TestConditionalRequests(t *testing.T) {
	var calls, notModified atomic.Int32
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("If-None-Match") == `"v1"` {
			notModified.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		fmt.Fprint(w, `[{"name":"v1.0.0"}]`)
	}, "")
	for i := 0; i < 3; i++ {
		b, err := c.fetch(context.Background(), "/repos/owner/repo/tags")
		if err != nil || !strings.Contains(string(b), "v1.0.0") {
			t.Fatalf("appel %d : %s, %v", i, b, err)
		}
	}
	if calls.Load() != 3 || notModified.Load() != 2 {
		t.Fatalf("appels=%d, 304=%d", calls.Load(), notModified.Load())
	}
}

// Le cache partagé évite même la requête conditionnelle pendant sa durée de vie.
func TestSharedCache(t *testing.T) {
	var calls atomic.Int32
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `[]`)
	}, "")
	for i := 0; i < 5; i++ {
		if _, err := c.Tags(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("%d requêtes pour 5 lectures dans la durée de vie du cache", calls.Load())
	}
}

// Un 404 (workflow pas encore créé) est mémorisé : pas de nouvelle requête à chaque poll.
func TestNotFoundIsRemembered(t *testing.T) {
	var calls atomic.Int32
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.NotFound(w, r)
	}, "")
	for i := 0; i < 3; i++ {
		if _, err := c.fetch(context.Background(), "/repos/owner/repo/actions/workflows/ci.yml/runs"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("%d requêtes pour un 404 déjà connu", calls.Load())
	}
}

// Quota épuisé : plus aucune requête jusqu'à la réinitialisation annoncée.
func TestRateLimitBlocks(t *testing.T) {
	var calls atomic.Int32
	reset := time.Now().Add(10 * time.Minute).Unix()
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprint(reset))
		w.WriteHeader(http.StatusForbidden)
	}, "")
	for i := 0; i < 3; i++ {
		if _, err := c.fetch(context.Background(), "/repos/owner/repo/tags"); !errors.Is(err, ErrRateLimited) {
			t.Fatalf("err = %v", err)
		}
	}
	if calls.Load() != 1 || c.RateRemaining() != 0 {
		t.Fatalf("appels=%d, quota=%d", calls.Load(), c.RateRemaining())
	}
}

func TestBodySizeLimit(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxBodyBytes+10)))
	}, "")
	if _, err := c.fetch(context.Background(), "/repos/owner/repo/tags"); err == nil {
		t.Fatal("une réponse trop volumineuse doit être refusée")
	}
}

func TestInputValidation(t *testing.T) {
	for _, repo := range []string{"", "owner", "owner/repo/../x", "own er/repo", "owner/re?po"} {
		if _, err := New(Options{Repo: repo, Loader: cache.NewLoader(cache.NewMemory(1), discard), Log: discard}); err == nil {
			t.Errorf("dépôt %q accepté", repo)
		}
	}
	if _, err := New(Options{BaseURL: "http://api.example.com", Repo: "o/r", Loader: cache.NewLoader(cache.NewMemory(1), discard), Log: discard}); err == nil {
		t.Error("URL d'API en HTTP clair acceptée (le jeton circulerait en clair)")
	}
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("aucune requête attendue") }, "")
	if _, err := c.WorkflowRuns(context.Background(), "../../x.yml", 1); err == nil {
		t.Error("nom de workflow invalide accepté")
	}
	if _, err := c.LatestCommit(context.Background(), "main?x=1"); err == nil {
		t.Error("branche invalide acceptée")
	}
}

func TestRunsAndJobs(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo/actions/workflows/ci.yml/runs":
			fmt.Fprint(w, `{"workflow_runs":[{"id":42,"run_number":7,"status":"in_progress","head_branch":"v1.0.0"}]}`)
		case "/repos/owner/repo/actions/runs/42/jobs":
			fmt.Fprint(w, `{"jobs":[{"id":1,"name":"build-scan","status":"in_progress","steps":[{"name":"Trivy","number":3,"status":"in_progress"}]}]}`)
		default:
			http.NotFound(w, r)
		}
	}, "")
	runs, err := c.WorkflowRuns(context.Background(), "ci.yml", 5)
	if err != nil || len(runs) != 1 || runs[0].RunNumber != 7 || runs[0].HeadBranch != "v1.0.0" {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
	jobs, err := c.Jobs(context.Background(), runs[0].ID)
	if err != nil || len(jobs) != 1 || jobs[0].Steps[0].Name != "Trivy" {
		t.Fatalf("jobs = %+v, %v", jobs, err)
	}
}

// TestRealAPI interroge la vraie API GitHub ; seulement si TEST_GITHUB_REPO est défini
// (jeton optionnel dans TEST_GITHUB_TOKEN).
func TestRealAPI(t *testing.T) {
	repo := os.Getenv("TEST_GITHUB_REPO")
	if repo == "" {
		t.Skip("TEST_GITHUB_REPO non défini")
	}
	c, err := New(Options{Repo: repo, Token: os.Getenv("TEST_GITHUB_TOKEN"),
		Loader: cache.NewLoader(cache.NewMemory(100), discard), Log: discard})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	commit, err := c.LatestCommit(ctx, "main")
	if err != nil || len(commit.SHA) != 40 {
		t.Fatalf("commit = %+v, %v", commit, err)
	}
	runs, err := c.RecentRuns(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) > 0 {
		if _, err := c.Jobs(ctx, runs[0].ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Tags(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("commit %s · %d runs · quota restant %d", commit.SHA[:7], len(runs), c.RateRemaining())
}
