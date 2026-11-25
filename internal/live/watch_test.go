package live

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatchDirSignalsOnceWhenStable(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	changes := make(chan struct{}, 10)
	go WatchDir(ctx, dir, 10*time.Millisecond, func() { changes <- struct{}{} })
	time.Sleep(30 * time.Millisecond)

	// Plusieurs écritures rapprochées (comme tsc) → un seul rechargement.
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte{byte(i)}, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-changes:
	case <-time.After(time.Second):
		t.Fatal("aucun changement signalé")
	}
	select {
	case <-changes:
		t.Fatal("changement signalé deux fois")
	case <-time.After(100 * time.Millisecond):
	}
}
