// Package store gère l'historique des déploiements.
// À l'étape 1 l'implémentation est en mémoire ; l'implémentation PostgreSQL
// (étape 3) respectera la même interface.
package store

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/model"
)

var ErrNotFound = errors.New("deployment not found")

type Store interface {
	ListDeployments(ctx context.Context, limit int) ([]model.Deployment, error)
	GetDeployment(ctx context.Context, id int64) (model.Deployment, error)
	CreateDeployment(ctx context.Context, d model.Deployment) (model.Deployment, error)
	Stats(ctx context.Context) (model.Stats, error)
}

// Memory est un Store en mémoire, protégé par un RWMutex.
type Memory struct {
	mu     sync.RWMutex
	nextID int64
	items  []model.Deployment // ordre d'insertion
}

func NewMemory() *Memory { return &Memory{nextID: 1} }

// ListDeployments renvoie les plus récents d'abord, sans le détail des steps.
func (m *Memory) ListDeployments(_ context.Context, limit int) ([]model.Deployment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]model.Deployment, 0, min(limit, len(m.items)))
	for i := len(m.items) - 1; i >= 0 && len(out) < limit; i-- {
		d := m.items[i]
		d.Steps = nil
		out = append(out, d)
	}
	return out, nil
}

func (m *Memory) GetDeployment(_ context.Context, id int64) (model.Deployment, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, d := range m.items {
		if d.ID == id {
			d.Steps = slices.Clone(d.Steps)
			return d, nil
		}
	}
	return model.Deployment{}, ErrNotFound
}

func (m *Memory) CreateDeployment(_ context.Context, d model.Deployment) (model.Deployment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d.ID = m.nextID
	m.nextID++
	d.Steps = slices.Clone(d.Steps)
	m.items = append(m.items, d)
	return d, nil
}

func (m *Memory) Stats(_ context.Context) (model.Stats, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return computeStats(m.items), nil
}

func computeStats(items []model.Deployment) model.Stats {
	s := model.Stats{ByTarget: map[string]int{}}
	var sumDur float64
	var signed int
	for i := range items {
		d := &items[i]
		s.Total++
		s.ByTarget[d.Target]++
		if d.Status == model.StatusSuccess {
			s.Succeeded++
			sumDur += d.DurationS
		} else {
			s.Failed++
		}
		if d.Signed {
			signed++
		}
		if s.LastDeployAt == nil || d.StartedAt.After(*s.LastDeployAt) {
			t := d.StartedAt
			s.LastDeployAt = &t
		}
	}
	if s.Total > 0 {
		s.FailureRate = float64(s.Failed) / float64(s.Total)
		s.SignedRate = float64(signed) / float64(s.Total)
	}
	if s.Succeeded > 0 {
		s.AvgDurationS = sumDur / float64(s.Succeeded)
	}
	return s
}

// WithHook décore un Store pour appeler onCreate après chaque insertion
// (utilisé pour pousser l'événement « deployment » sur le flux SSE).
func WithHook(s Store, onCreate func(model.Deployment)) Store {
	return &hooked{Store: s, onCreate: onCreate}
}

type hooked struct {
	Store
	onCreate func(model.Deployment)
}

func (h *hooked) CreateDeployment(ctx context.Context, d model.Deployment) (model.Deployment, error) {
	created, err := h.Store.CreateDeployment(ctx, d)
	if err == nil && h.onCreate != nil {
		h.onCreate(created)
	}
	return created, err
}
