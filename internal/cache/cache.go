// Package cache fournit un cache clé/valeur à durée de vie courte (Redis, ou
// mémoire à défaut) et un Loader qui l'utilise devant une source lente ou
// soumise à quota (API GitHub/Azure, base de données).
//
// Le cache est une optimisation, jamais une dépendance dure : si Redis est
// indisponible, le Loader interroge directement la source (fail-open).
package cache

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// Cache est implémenté par Redis et Memory. Les clés sont préfixées par
// l'implémentation (espace de noms « cmx:v1: »).
type Cache interface {
	Get(ctx context.Context, key string) (val []byte, found bool, err error)
	Set(ctx context.Context, key string, val []byte, ttl time.Duration) error
	Incr(ctx context.Context, key string) (int64, error)
	Close() error
}

const (
	keyPrefix = "cmx:v1:"
	// MaxValueBytes : au-delà, la valeur n'est pas mise en cache (évite qu'une
	// réponse anormalement grosse remplisse Redis).
	MaxValueBytes = 1 << 20
)

// Stats compte les accès au cache (exposé pour le suivi).
type Stats struct {
	Hits, Misses, Errors int64
}

// Loader lit dans le cache et, en cas d'absence, appelle la source une seule
// fois même si plusieurs requêtes arrivent en même temps (singleflight).
type Loader struct {
	c     Cache
	log   *slog.Logger
	group singleflight.Group

	hits, misses, errs atomic.Int64

	mu       sync.Mutex
	lastWarn time.Time
}

func NewLoader(c Cache, log *slog.Logger) *Loader {
	return &Loader{c: c, log: log}
}

func (l *Loader) Stats() Stats {
	return Stats{Hits: l.hits.Load(), Misses: l.misses.Load(), Errors: l.errs.Load()}
}

// Get renvoie la valeur en cache, ou appelle fetch et met le résultat en cache
// pour ttl. Les erreurs de fetch ne sont jamais mises en cache.
func (l *Loader) Get(ctx context.Context, key string, ttl time.Duration, fetch func(context.Context) ([]byte, error)) ([]byte, error) {
	if b, ok, err := l.c.Get(ctx, key); err != nil {
		l.fail(err)
	} else if ok {
		l.hits.Add(1)
		return b, nil
	}
	l.misses.Add(1)
	v, err, _ := l.group.Do(key, func() (any, error) {
		b, err := fetch(ctx)
		if err != nil {
			return nil, err
		}
		if len(b) <= MaxValueBytes {
			if err := l.c.Set(ctx, key, b, ttl); err != nil {
				l.fail(err)
			}
		}
		return b, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]byte), nil
}

// Generation renvoie le numéro de génération d'un groupe de clés. Inclure ce
// numéro dans les clés permet d'invalider tout le groupe d'un coup (Bump),
// y compris pour les autres réplicas qui partagent le même Redis.
// ok=false si le cache est indisponible : l'appelant doit alors s'en passer.
func (l *Loader) Generation(ctx context.Context, group string) (gen int64, ok bool) {
	b, found, err := l.c.Get(ctx, "gen:"+group)
	if err != nil {
		l.fail(err)
		return 0, false
	}
	if !found {
		return 0, true
	}
	n, err := strconv.ParseInt(string(b), 10, 64)
	return n, err == nil
}

// Bump invalide toutes les clés d'un groupe en passant à la génération suivante.
func (l *Loader) Bump(ctx context.Context, group string) {
	if _, err := l.c.Incr(ctx, "gen:"+group); err != nil {
		l.fail(err)
	}
}

// fail compte l'erreur et la journalise au plus une fois toutes les 30 s,
// pour ne pas inonder les logs si Redis est arrêté.
func (l *Loader) fail(err error) {
	l.errs.Add(1)
	l.mu.Lock()
	defer l.mu.Unlock()
	if time.Since(l.lastWarn) > 30*time.Second {
		l.lastWarn = time.Now()
		l.log.Warn("cache indisponible, accès direct à la source", "err", err)
	}
}
