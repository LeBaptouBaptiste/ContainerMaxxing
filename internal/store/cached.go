package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/cache"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/model"
)

const (
	listTTL   = 30 * time.Second // liste et stats : invalidées à chaque nouveau déploiement
	detailTTL = 5 * time.Minute  // un déploiement enregistré ne change plus
	cacheGrp  = "deployments"
)

// WithCache met en cache les lectures de l'historique (liste, détail, stats).
// Les clés de liste/stats contiennent un numéro de génération incrémenté à
// chaque CreateDeployment : la nouvelle donnée est visible immédiatement, sur
// toutes les instances qui partagent le même Redis.
func WithCache(s Store, l *cache.Loader) Store {
	return &cached{Store: s, l: l}
}

type cached struct {
	Store
	l *cache.Loader
}

func (c *cached) ListDeployments(ctx context.Context, limit int) ([]model.Deployment, error) {
	gen, ok := c.l.Generation(ctx, cacheGrp)
	if !ok {
		return c.Store.ListDeployments(ctx, limit) // cache indisponible : accès direct
	}
	var out []model.Deployment
	err := c.load(ctx, fmt.Sprintf("deployments:list:%d:%d", gen, limit), listTTL, &out, func(ctx context.Context) (any, error) {
		return c.Store.ListDeployments(ctx, limit)
	})
	return out, err
}

func (c *cached) GetDeployment(ctx context.Context, id int64) (model.Deployment, error) {
	var d model.Deployment
	err := c.load(ctx, fmt.Sprintf("deployments:id:%d", id), detailTTL, &d, func(ctx context.Context) (any, error) {
		return c.Store.GetDeployment(ctx, id)
	})
	return d, err
}

func (c *cached) Stats(ctx context.Context) (model.Stats, error) {
	gen, ok := c.l.Generation(ctx, cacheGrp)
	if !ok {
		return c.Store.Stats(ctx)
	}
	var st model.Stats
	err := c.load(ctx, fmt.Sprintf("deployments:stats:%d", gen), listTTL, &st, func(ctx context.Context) (any, error) {
		return c.Store.Stats(ctx)
	})
	return st, err
}

func (c *cached) CreateDeployment(ctx context.Context, d model.Deployment) (model.Deployment, error) {
	created, err := c.Store.CreateDeployment(ctx, d)
	if err == nil {
		c.l.Bump(ctx, cacheGrp)
	}
	return created, err
}

// load passe par le Loader en sérialisant la valeur en JSON.
func (c *cached) load(ctx context.Context, key string, ttl time.Duration, dst any, fetch func(context.Context) (any, error)) error {
	b, err := c.l.Get(ctx, key, ttl, func(ctx context.Context) ([]byte, error) {
		v, err := fetch(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(v)
	})
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}
