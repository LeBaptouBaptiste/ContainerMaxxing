// Commande server : API + flux SSE + frontend embarqué, en un seul binaire statique.
//
// Usage :
//
//	server              démarre le serveur
//	server healthcheck  interroge /healthz (pour HEALTHCHECK : l'image distroless n'a ni shell ni curl)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/api"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/cache"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/config"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/github"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/live"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/source"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/store"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/web"
)

// Injectés à la compilation : -ldflags "-X main.version=v1.2.3 -X main.commit=abc".
var (
	version = "dev"
	commit  = "none"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLevel(cfg.LogLevel)}))
	log.Info("starting", "version", version, "commit", commit, "env", cfg.Environment,
		"source", cfg.DataSource, "poll", cfg.PollInterval.String(), "ingest", cfg.IngestToken != "")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	hub := live.NewHub(log, 100)

	var base store.Store = store.NewMemory()
	if cfg.DatabaseURL != "" {
		pg, err := store.OpenPostgres(ctx, cfg.DatabaseURL, log)
		if err != nil {
			return err
		}
		defer pg.Close()
		base = pg
		log.Info("historique stocké dans PostgreSQL")
	} else {
		log.Warn("DATABASE_URL absent : historique en mémoire, perdu au redémarrage")
	}

	// Cache des lectures (et, plus tard, des API GitHub/Azure). Redis s'il est
	// configuré, sinon mémoire locale. Jamais bloquant : si Redis tombe, l'app
	// interroge directement la source.
	var kv cache.Cache = cache.NewMemory(1000)
	if cfg.RedisURL != "" {
		r, err := cache.OpenRedis(ctx, cfg.RedisURL, log)
		if err != nil {
			return err
		}
		defer r.Close()
		kv = r
		log.Info("cache partagé dans Redis")
	} else {
		log.Info("REDIS_URL absent : cache en mémoire du processus")
	}
	loader := cache.NewLoader(kv, log)

	// Ordre des décorateurs : le cache est invalidé avant que le hook ne prévienne
	// les navigateurs, qui relisent alors une liste à jour.
	st := store.WithHook(store.WithCache(base, loader), hub.PublishDeployment)

	// Source des données : simulation (mock, avec mode démo) ou API GitHub (live).
	var src source.Source
	var demo api.DemoController
	switch cfg.DataSource {
	case "live":
		gh, err := github.New(github.Options{Repo: cfg.GitHubRepo, Token: cfg.GitHubToken, Loader: loader, Log: log})
		if err != nil {
			return err
		}
		if cfg.GitHubToken == "" {
			log.Warn("GITHUB_TOKEN absent : quota de 60 requêtes/h, essai local uniquement")
		}
		src = source.NewGitHub(cfg, gh, version)
		log.Info("données en direct depuis l'API GitHub", "repo", cfg.GitHubRepo, "workflow", cfg.GitHubWorkflow)
	default:
		mock := source.NewMock(cfg, st, version)
		if err := mock.Seed(ctx); err != nil {
			return fmt.Errorf("seed: %w", err)
		}
		if cfg.DemoEnabled() {
			mock.EnableDemo(cfg.DemoToken != "")
			demo = mock
			log.Info("demo controls enabled", "token_required", cfg.DemoToken != "")
		}
		src = mock
	}
	go hub.Run(ctx, src, cfg.PollInterval)

	// En prod, le frontend est embarqué dans le binaire. En mode dev, il est lu
	// sur le disque et toute modification recharge les navigateurs ouverts.
	static, bootID := web.Static(), ""
	if cfg.DevMode {
		static = os.DirFS(cfg.StaticDir)
		bootID = strconv.FormatInt(time.Now().UnixNano(), 36)
		go live.WatchDir(ctx, cfg.StaticDir, 400*time.Millisecond, hub.PublishReload)
		log.Warn("DEV MODE: frontend lu sur le disque, rechargement automatique actif", "dir", cfg.StaticDir)
	}

	srv := &http.Server{
		Addr: cfg.ListenAddr,
		Handler: api.New(api.Options{
			Log: log, Hub: hub, Store: st, IngestToken: cfg.IngestToken,
			Static: static, Done: ctx.Done(), DevBootID: bootID,
			Demo: demo, DemoToken: cfg.DemoToken,
		}).Handler(),
		ReadHeaderTimeout: 5 * time.Second, // anti-slowloris
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second, // le flux SSE repousse sa propre échéance
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.ListenAddr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func healthcheck() int {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + addr + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "status", resp.StatusCode)
		return 1
	}
	return 0
}

func parseLevel(s string) slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return slog.LevelInfo
	}
	return l
}
