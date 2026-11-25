// Package api expose l'API JSON, le flux SSE et le frontend statique.
package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/live"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/model"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/store"
)

const maxBodyBytes = 64 << 10

type Server struct {
	log         *slog.Logger
	hub         *live.Hub
	store       store.Store
	ingestToken [32]byte // haché : comparaison à temps constant indépendante de la longueur
	ingestOn    bool
	static      fs.FS
	done        <-chan struct{} // fermé à l'arrêt : termine les flux SSE
}

type Options struct {
	Log         *slog.Logger
	Hub         *live.Hub
	Store       store.Store
	IngestToken string
	Static      fs.FS
	Done        <-chan struct{}
}

func New(o Options) *Server {
	return &Server{
		log: o.Log, hub: o.Hub, store: o.Store, static: o.Static, done: o.Done,
		ingestToken: sha256.Sum256([]byte(o.IngestToken)), ingestOn: o.IngestToken != "",
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/stream", s.stream)
	mux.HandleFunc("GET /api/deployments", s.listDeployments)
	mux.HandleFunc("GET /api/deployments/{id}", s.getDeployment)
	mux.HandleFunc("POST /api/deployments", s.createDeployment)
	mux.HandleFunc("GET /api/stats", s.stats)
	mux.Handle("GET /", staticHandler(s.static))
	return s.recoverer(s.logRequests(securityHeaders(mux)))
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	if s.hub.Latest() == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	b := s.hub.Latest()
	if b == nil {
		writeError(w, http.StatusServiceUnavailable, "état pas encore disponible")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

func (s *Server) listDeployments(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			writeError(w, http.StatusBadRequest, "limit doit être entre 1 et 200")
			return
		}
		limit = n
	}
	items, err := s.store.ListDeployments(r.Context(), limit)
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getDeployment(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "id invalide")
		return
	}
	d, err := s.store.GetDeployment(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "déploiement introuvable")
		return
	}
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.Stats(r.Context())
	if err != nil {
		s.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// createDeployment est appelé par la CI en fin de run.
// Authentification par jeton Bearer, corps borné, champs inconnus refusés,
// validation stricte de chaque champ.
func (s *Server) createDeployment(w http.ResponseWriter, r *http.Request) {
	if !s.ingestOn {
		writeError(w, http.StatusForbidden, "ingestion désactivée (INGEST_TOKEN non configuré)")
		return
	}
	if !s.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="ingest"`)
		writeError(w, http.StatusUnauthorized, "jeton invalide")
		return
	}
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type application/json attendu")
		return
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	var d model.Deployment
	if err := dec.Decode(&d); err != nil {
		writeError(w, http.StatusBadRequest, "JSON invalide")
		return
	}
	d.ID = 0
	if err := d.Validate(); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	created, err := s.store.CreateDeployment(r.Context(), d)
	if err != nil {
		s.internalError(w, err)
		return
	}
	s.log.Info("deployment recorded", "id", created.ID, "tag", created.Tag, "target", created.Target)
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) authorized(r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || tok == "" {
		return false
	}
	got := sha256.Sum256([]byte(tok))
	return subtle.ConstantTimeCompare(got[:], s.ingestToken[:]) == 1
}

// stream diffuse les snapshots en Server-Sent Events.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	events, cancel, err := s.hub.Subscribe()
	if err != nil {
		w.Header().Set("Retry-After", "10")
		writeError(w, http.StatusServiceUnavailable, "trop de clients connectés")
		return
	}
	defer cancel()

	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // désactive le buffering des reverse proxies (NPM/nginx)
	w.WriteHeader(http.StatusOK)

	send := func(event string, data []byte) error {
		// Le WriteTimeout global du serveur couperait le flux : on repousse
		// l'échéance à chaque écriture, ce qui borne quand même un client bloqué.
		_ = rc.SetWriteDeadline(time.Now().Add(15 * time.Second))
		if event != "" {
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
				return err
			}
		} else if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
			return err
		}
		return rc.Flush()
	}

	if b := s.hub.Latest(); b != nil {
		if send("status", b) != nil {
			return
		}
	}
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.done:
			return
		case e := <-events:
			if send(e.Name, e.Data) != nil {
				return
			}
		case <-ping.C:
			if send("", nil) != nil {
				return
			}
		}
	}
}

func (s *Server) internalError(w http.ResponseWriter, err error) {
	// Le détail reste dans les logs serveur ; le client reçoit un message générique.
	s.log.Error("internal error", "err", err)
	writeError(w, http.StatusInternalServerError, "erreur interne")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// --- middlewares ---------------------------------------------------------------

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// Tout est servi depuis la même origine : pas de CDN, pas de script inline.
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(code int)        { s.code = code; s.ResponseWriter.WriteHeader(code) }
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/healthz" {
			return
		}
		// Pas de query string ni d'en-têtes dans les logs (jetons, données perso).
		s.log.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.code, "dur_ms", time.Since(start).Milliseconds())
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.log.Error("panic", "value", fmt.Sprint(v), "path", r.URL.Path)
				writeError(w, http.StatusInternalServerError, "erreur interne")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func staticHandler(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fichiers non versionnés : on force la revalidation.
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}
