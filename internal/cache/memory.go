package cache

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// Memory est un cache local au processus, utilisé quand REDIS_URL n'est pas
// défini (développement, ou une seule instance). Taille bornée.
type Memory struct {
	mu      sync.Mutex
	max     int
	entries map[string]memEntry
	now     func() time.Time
}

type memEntry struct {
	val []byte
	exp time.Time // zéro : pas d'expiration
}

func NewMemory(maxEntries int) *Memory {
	return &Memory{max: maxEntries, entries: map[string]memEntry{}, now: time.Now}
}

func (m *Memory) Get(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[keyPrefix+key]
	if !ok || (!e.exp.IsZero() && m.now().After(e.exp)) {
		return nil, false, nil
	}
	return e.val, true, nil
}

func (m *Memory) Set(_ context.Context, key string, val []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.entries) >= m.max {
		m.evict()
	}
	m.entries[keyPrefix+key] = memEntry{val: val, exp: m.now().Add(ttl)}
	return nil
}

func (m *Memory) Incr(_ context.Context, key string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[keyPrefix+key]
	n, _ := strconv.ParseInt(string(e.val), 10, 64)
	n++
	m.entries[keyPrefix+key] = memEntry{val: []byte(strconv.FormatInt(n, 10))}
	return n, nil
}

func (m *Memory) Close() error { return nil }

// evict retire les entrées expirées, puis au besoin des entrées arbitraires
// (le cache reste borné quoi qu'il arrive).
func (m *Memory) evict() {
	now := m.now()
	for k, e := range m.entries {
		if !e.exp.IsZero() && now.After(e.exp) {
			delete(m.entries, k)
		}
	}
	for k, e := range m.entries {
		if len(m.entries) < m.max {
			return
		}
		if !e.exp.IsZero() { // on garde les compteurs de génération
			delete(m.entries, k)
		}
	}
}
