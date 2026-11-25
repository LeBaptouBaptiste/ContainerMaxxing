// Package live interroge la source à intervalle fixe et diffuse le dernier
// état aux clients SSE.
package live

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/model"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/source"
)

// ErrTooManyClients est renvoyée quand la limite de connexions SSE est atteinte
// (protection contre l'épuisement de ressources : chaque flux garde une
// connexion et une goroutine ouvertes).
var ErrTooManyClients = errors.New("too many stream clients")

type Event struct {
	Name string // status | deployment
	Data []byte // JSON
}

type Hub struct {
	log     *slog.Logger
	maxSubs int

	mu     sync.RWMutex
	latest []byte
	subs   map[chan Event]struct{}
}

func NewHub(log *slog.Logger, maxSubs int) *Hub {
	return &Hub{log: log, maxSubs: maxSubs, subs: map[chan Event]struct{}{}}
}

// Run interroge src immédiatement puis à chaque intervalle, jusqu'à l'annulation de ctx.
func (h *Hub) Run(ctx context.Context, src source.Source, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		h.poll(ctx, src, interval)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (h *Hub) poll(ctx context.Context, src source.Source, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	snap, err := src.Poll(ctx)
	if err != nil {
		// On garde le dernier état connu plutôt que d'afficher une page vide.
		h.log.Warn("poll failed", "err", err)
	}
	if len(snap.Stages) == 0 {
		return
	}
	b, err := json.Marshal(snap)
	if err != nil {
		h.log.Error("marshal snapshot", "err", err)
		return
	}
	h.mu.Lock()
	h.latest = b
	h.mu.Unlock()
	h.broadcast(Event{Name: "status", Data: b})
}

// Latest renvoie le dernier snapshot JSON (nil avant le premier poll).
func (h *Hub) Latest() []byte {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.latest
}

// PublishDeployment notifie les clients qu'un déploiement a été enregistré.
func (h *Hub) PublishDeployment(d model.Deployment) {
	d.Steps = nil
	b, err := json.Marshal(d)
	if err != nil {
		return
	}
	h.broadcast(Event{Name: "deployment", Data: b})
}

// PublishReload demande aux navigateurs de recharger la page (mode dev).
func (h *Hub) PublishReload() {
	h.broadcast(Event{Name: "reload", Data: []byte("{}")})
}

func (h *Hub) Subscribe() (<-chan Event, func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subs) >= h.maxSubs {
		return nil, nil, ErrTooManyClients
	}
	ch := make(chan Event, 4)
	h.subs[ch] = struct{}{}
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
		})
	}, nil
}

// broadcast n'est jamais bloquant : un client lent perd des événements
// intermédiaires (il recevra le prochain snapshot complet) au lieu de
// ralentir tous les autres.
func (h *Hub) broadcast(e Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs {
		select {
		case ch <- e:
		default:
		}
	}
}
