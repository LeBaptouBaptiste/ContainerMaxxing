package cache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestLoaderHitMissAndTTL(t *testing.T) {
	ctx := context.Background()
	mem := NewMemory(100)
	clock := time.Now()
	mem.now = func() time.Time { return clock }
	l := NewLoader(mem, discard)

	var calls int
	fetch := func(context.Context) ([]byte, error) { calls++; return []byte(fmt.Sprint(calls)), nil }

	a, _ := l.Get(ctx, "k", time.Second, fetch)
	b, _ := l.Get(ctx, "k", time.Second, fetch)
	if string(a) != "1" || string(b) != "1" || calls != 1 {
		t.Fatalf("a=%s b=%s calls=%d : le second appel devait venir du cache", a, b, calls)
	}
	clock = clock.Add(2 * time.Second) // expiration
	c, _ := l.Get(ctx, "k", time.Second, fetch)
	if string(c) != "2" {
		t.Fatalf("après expiration : %s", c)
	}
	if st := l.Stats(); st.Hits != 1 || st.Misses != 2 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestLoaderDoesNotCacheErrors(t *testing.T) {
	l := NewLoader(NewMemory(10), discard)
	var calls int
	fetch := func(context.Context) ([]byte, error) { calls++; return nil, errors.New("API indisponible") }
	for i := 0; i < 2; i++ {
		if _, err := l.Get(context.Background(), "k", time.Minute, fetch); err == nil {
			t.Fatal("erreur attendue")
		}
	}
	if calls != 2 {
		t.Fatalf("une erreur ne doit pas être mise en cache (calls=%d)", calls)
	}
}

// Plusieurs requêtes simultanées sur une clé absente → un seul appel à la source.
func TestLoaderSingleflight(t *testing.T) {
	l := NewLoader(NewMemory(10), discard)
	var calls atomic.Int32
	release := make(chan struct{})
	fetch := func(context.Context) ([]byte, error) {
		calls.Add(1)
		<-release
		return []byte("ok"), nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = l.Get(context.Background(), "k", time.Minute, fetch) }()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("la source a été appelée %d fois", calls.Load())
	}
}

type brokenCache struct{}

var errDown = errors.New("connection refused")

func (brokenCache) Get(context.Context, string) ([]byte, bool, error)        { return nil, false, errDown }
func (brokenCache) Set(context.Context, string, []byte, time.Duration) error { return errDown }
func (brokenCache) Incr(context.Context, string) (int64, error)              { return 0, errDown }
func (brokenCache) Close() error                                             { return nil }

// Redis arrêté : l'app doit continuer en interrogeant directement la source.
func TestLoaderFailOpen(t *testing.T) {
	l := NewLoader(brokenCache{}, discard)
	b, err := l.Get(context.Background(), "k", time.Minute, func(context.Context) ([]byte, error) { return []byte("source"), nil })
	if err != nil || string(b) != "source" {
		t.Fatalf("b=%s err=%v", b, err)
	}
	if _, ok := l.Generation(context.Background(), "g"); ok {
		t.Fatal("Generation doit signaler que le cache est indisponible")
	}
	if l.Stats().Errors == 0 {
		t.Fatal("les erreurs de cache doivent être comptées")
	}
}

func TestGenerationBump(t *testing.T) {
	ctx := context.Background()
	l := NewLoader(NewMemory(10), discard)
	g0, ok := l.Generation(ctx, "deployments")
	l.Bump(ctx, "deployments")
	g1, _ := l.Generation(ctx, "deployments")
	if !ok || g0 != 0 || g1 != 1 {
		t.Fatalf("générations %d → %d (ok=%v)", g0, g1, ok)
	}
}

func TestMemoryIsBounded(t *testing.T) {
	m := NewMemory(5)
	for i := 0; i < 50; i++ {
		_ = m.Set(context.Background(), fmt.Sprint(i), []byte("x"), time.Minute)
	}
	if len(m.entries) > 5 {
		t.Fatalf("%d entrées pour une limite de 5", len(m.entries))
	}
}

// TestRedis tourne seulement si TEST_REDIS_URL est défini (ex. redis://redis:6379/15).
func TestRedis(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("TEST_REDIS_URL non défini")
	}
	ctx := context.Background()
	r, err := OpenRedis(ctx, url, discard)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	key := fmt.Sprintf("test:%d", time.Now().UnixNano())
	if _, ok, err := r.Get(ctx, key); ok || err != nil {
		t.Fatalf("clé absente : ok=%v err=%v", ok, err)
	}
	if err := r.Set(ctx, key, []byte("v"), 200*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if b, ok, _ := r.Get(ctx, key); !ok || string(b) != "v" {
		t.Fatalf("get = %s, %v", b, ok)
	}
	time.Sleep(300 * time.Millisecond)
	if _, ok, _ := r.Get(ctx, key); ok {
		t.Fatal("la clé aurait dû expirer")
	}
	if n, err := r.Incr(ctx, key+":gen"); err != nil || n != 1 {
		t.Fatalf("incr = %d, %v", n, err)
	}
}
