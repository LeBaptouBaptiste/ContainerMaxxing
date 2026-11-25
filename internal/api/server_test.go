package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/config"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/live"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/source"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/store"
)

const token = "0123456789abcdef0123456789abcdef"

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := live.NewHub(log, 2)
	st := store.WithHook(store.NewMemory(), hub.PublishDeployment)
	cfg := config.Config{Environment: "local", GitHubRepo: "o/r", ACRName: "acr", CaaSApp: "app"}
	mock := source.NewMock(cfg, st, "test")
	if err := mock.Seed(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go hub.Run(ctx, mock, time.Hour)
	for hub.Latest() == nil {
		time.Sleep(5 * time.Millisecond)
	}
	srv := httptest.NewServer(New(Options{
		Log: log, Hub: hub, Store: st, IngestToken: token, Done: ctx.Done(),
		Static: fstest.MapFS{"index.html": {Data: []byte("<!doctype html>")}},
	}).Handler())
	t.Cleanup(srv.Close)
	return srv
}

const validBody = `{"tag":"v9.9.9","digest":"sha256:` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" +
	`","status":"success","cve_critical":0,"cve_high":0,"cve_medium":1,"dockle_status":"pass","signed":true,` +
	`"target":"azure","run_url":"https://github.com/o/r/actions/runs/1","started_at":"2026-01-01T10:00:00Z","duration_s":120}`

func post(t *testing.T, url, auth, body string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url+"/api/deployments", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestIngest(t *testing.T) {
	srv := newTestServer(t)
	cases := []struct {
		name, auth, body string
		want             int
	}{
		{"sans jeton", "", validBody, http.StatusUnauthorized},
		{"mauvais jeton", "Bearer nope", validBody, http.StatusUnauthorized},
		{"valide", "Bearer " + token, validBody, http.StatusCreated},
		{"champ inconnu", "Bearer " + token, strings.Replace(validBody, `"signed"`, `"admin":true,"signed"`, 1), http.StatusBadRequest},
		{"lien javascript", "Bearer " + token, strings.Replace(validBody, "https://github.com/o/r/actions/runs/1", "javascript:alert(1)", 1), http.StatusUnprocessableEntity},
		{"cible inconnue", "Bearer " + token, strings.Replace(validBody, `"azure"`, `"aws"`, 1), http.StatusUnprocessableEntity},
		{"corps trop gros", "Bearer " + token, `{"tag":"` + strings.Repeat("a", 70000) + `"}`, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := post(t, srv.URL, c.auth, c.body); got != c.want {
				t.Fatalf("status = %d, want %d", got, c.want)
			}
		})
	}
}

func TestSecurityHeadersAndStatus(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Fatalf("CSP manquante: %q", csp)
	}
	b, _ := io.ReadAll(resp.Body)
	for _, id := range []string{"image", "phobos", "compose", "github", "actions", "acr", "caas"} {
		if !strings.Contains(string(b), `"id":"`+id+`"`) {
			t.Errorf("étape %s absente", id)
		}
	}
}

func TestStreamLimit(t *testing.T) {
	srv := newTestServer(t) // maxSubs = 2
	var open []*http.Response
	for i := 0; i < 3; i++ {
		resp, err := http.Get(srv.URL + "/api/stream")
		if err != nil {
			t.Fatal(err)
		}
		open = append(open, resp)
	}
	defer func() {
		for _, r := range open {
			r.Body.Close()
		}
	}()
	if open[0].StatusCode != 200 || open[2].StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("codes = %d, %d", open[0].StatusCode, open[2].StatusCode)
	}
	if ct := open[0].Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}
}
