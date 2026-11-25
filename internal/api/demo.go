package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/model"
)

// DemoController pilote la release simulée (implémenté par source.Mock).
type DemoController interface {
	Trigger(scenario string) error
	SetPaused(paused bool)
	Step(ctx context.Context) error
	Snapshot() model.Snapshot
}

func (s *Server) demoRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/demo/release", s.demoGuard(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Scenario string `json:"scenario"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "JSON invalide")
			return
		}
		if err := s.demo.Trigger(body.Scenario); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "scénario inconnu")
			return
		}
		s.log.Info("demo: release déclenchée", "scenario", body.Scenario)
		s.demoDone(w)
	}))
	mux.HandleFunc("POST /api/demo/pause", s.demoGuard(func(w http.ResponseWriter, _ *http.Request) {
		s.demo.SetPaused(true)
		s.demoDone(w)
	}))
	mux.HandleFunc("POST /api/demo/play", s.demoGuard(func(w http.ResponseWriter, _ *http.Request) {
		s.demo.SetPaused(false)
		s.demoDone(w)
	}))
	mux.HandleFunc("POST /api/demo/step", s.demoGuard(func(w http.ResponseWriter, r *http.Request) {
		if err := s.demo.Step(r.Context()); err != nil {
			s.internalError(w, err)
			return
		}
		s.demoDone(w)
	}))
}

// demoDone diffuse immédiatement le nouvel état à tous les navigateurs.
func (s *Server) demoDone(w http.ResponseWriter) {
	s.hub.Publish(s.demo.Snapshot())
	w.WriteHeader(http.StatusNoContent)
}

// demoGuard protège les commandes de démo, qui modifient l'état du serveur :
//   - Content-Type application/json obligatoire : un formulaire d'un autre site
//     ne peut pas l'envoyer sans requête préliminaire CORS, que l'on n'accepte pas
//     (protection CSRF) ;
//   - si l'en-tête Origin est présent, il doit correspondre à l'hôte servi ;
//   - jeton Bearer exigé quand DEMO_TOKEN est configuré (hors poste local).
func (s *Server) demoGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			writeError(w, http.StatusUnsupportedMediaType, "Content-Type application/json attendu")
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			if u, err := url.Parse(o); err != nil || u.Host != r.Host {
				writeError(w, http.StatusForbidden, "origine refusée")
				return
			}
		}
		if s.demoAuth && !bearerMatches(r, s.demoToken) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="demo"`)
			writeError(w, http.StatusUnauthorized, "jeton de démo invalide")
			return
		}
		next(w, r)
	}
}
