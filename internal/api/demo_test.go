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

func newDemoServer(t *testing.T, demoToken string, enabled bool) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := live.NewHub(log, 4)
	st := store.NewMemory()
	mock := source.NewMock(config.Config{Environment: "local", GitHubRepo: "o/r", GHCRImage: "ghcr.io/o/r", CaaSApp: "c"}, st, "test")
	if err := mock.Seed(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go hub.Run(ctx, mock, time.Hour)
	opts := Options{Log: log, Hub: hub, Store: st, Done: ctx.Done(), Static: fstest.MapFS{}}
	if enabled {
		mock.EnableDemo(demoToken != "")
		opts.Demo, opts.DemoToken = mock, demoToken
	}
	srv := httptest.NewServer(New(opts).Handler())
	t.Cleanup(srv.Close)
	return srv
}

func demoPost(t *testing.T, url, path, body string, headers map[string]string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url+path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestDemoGuards(t *testing.T) {
	const tok = "demo-demo-demo-demo-demo-demo-demo-demo"
	jsonCT := map[string]string{"Content-Type": "application/json"}
	withTok := map[string]string{"Content-Type": "application/json", "Authorization": "Bearer " + tok}

	local := newDemoServer(t, "", true)
	remote := newDemoServer(t, tok, true)
	off := newDemoServer(t, "", false)

	cases := []struct {
		name    string
		srv     *httptest.Server
		path    string
		body    string
		headers map[string]string
		want    int
	}{
		{"local sans jeton", local, "/api/demo/pause", "{}", jsonCT, http.StatusNoContent},
		{"scénario valide", local, "/api/demo/release", `{"scenario":"cve"}`, jsonCT, http.StatusNoContent},
		{"scénario inconnu", local, "/api/demo/release", `{"scenario":"nope"}`, jsonCT, http.StatusUnprocessableEntity},
		{"champ inconnu", local, "/api/demo/release", `{"scenario":"cve","x":1}`, jsonCT, http.StatusBadRequest},
		{"formulaire (CSRF)", local, "/api/demo/step", "a=b", map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, http.StatusUnsupportedMediaType},
		{"autre origine", local, "/api/demo/step", "{}", map[string]string{"Content-Type": "application/json", "Origin": "https://evil.example"}, http.StatusForbidden},
		{"jeton exigé absent", remote, "/api/demo/step", "{}", jsonCT, http.StatusUnauthorized},
		{"jeton exigé valide", remote, "/api/demo/step", "{}", withTok, http.StatusNoContent},
		{"démo désactivée", off, "/api/demo/step", "{}", jsonCT, http.StatusMethodNotAllowed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := demoPost(t, c.srv.URL, c.path, c.body, c.headers); got != c.want {
				t.Fatalf("status = %d, want %d", got, c.want)
			}
		})
	}
}
