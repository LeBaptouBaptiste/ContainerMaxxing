package cache

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// opTimeout borne chaque commande : un Redis lent ne doit pas ralentir les
// requêtes HTTP (au pire, on passe directement à la source).
const opTimeout = 500 * time.Millisecond

// Redis est le cache partagé entre instances (réplicas Container Apps, ou
// plusieurs conteneurs app) : un seul appel à l'API amont par durée de vie.
type Redis struct {
	client *redis.Client
}

// OpenRedis ne bloque pas le démarrage si Redis est indisponible : le client se
// reconnecte tout seul et, en attendant, le Loader interroge la source.
// L'URL (qui peut contenir le mot de passe) n'est jamais journalisée.
func OpenRedis(ctx context.Context, url string, log *slog.Logger) (*Redis, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, errors.New("REDIS_URL invalide") // l'erreur d'origine pourrait citer l'URL
	}
	opt.DialTimeout = 2 * time.Second
	opt.ReadTimeout = opTimeout
	opt.WriteTimeout = opTimeout
	opt.PoolSize = 10
	opt.MaxRetries = 1
	opt.ClientName = "containermaxxing"

	r := &Redis{client: redis.NewClient(opt)}
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := r.client.Ping(pingCtx).Err(); err != nil {
		log.Warn("redis injoignable au démarrage, l'app continue sans cache partagé", "err", err)
	}
	return r, nil
}

func (r *Redis) Get(ctx context.Context, key string) ([]byte, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	b, err := r.client.Get(ctx, keyPrefix+key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return b, true, nil
}

func (r *Redis) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	return r.client.Set(ctx, keyPrefix+key, val, ttl).Err()
}

func (r *Redis) Incr(ctx context.Context, key string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	return r.client.Incr(ctx, keyPrefix+key).Result()
}

func (r *Redis) Close() error { return r.client.Close() }
