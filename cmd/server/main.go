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
	"strings"
	"syscall"
	"time"

	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/api"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/config"
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
	st := store.WithHook(store.NewMemory(), hub.PublishDeployment)

	mock := source.NewMock(cfg, st, version)
	if err := mock.Seed(ctx); err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	go hub.Run(ctx, mock, cfg.PollInterval)

	srv := &http.Server{
		Addr: cfg.ListenAddr,
		Handler: api.New(api.Options{
			Log: log, Hub: hub, Store: st, IngestToken: cfg.IngestToken,
			Static: web.Static(), Done: ctx.Done(),
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
