package live

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"
)

// WatchDir appelle onChange quand un fichier de dir change (mode dev uniquement).
// Surveillance par scrutation et non par inotify : les montages de volumes
// depuis Windows/macOS ne remontent pas les événements fichiers au conteneur.
// Un changement n'est signalé qu'une fois le dossier stable pendant un tour,
// pour ne pas recharger plusieurs fois quand tsc écrit plusieurs fichiers.
func WatchDir(ctx context.Context, dir string, every time.Duration, onChange func()) {
	last := fingerprint(dir)
	pending := false
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		fp := fingerprint(dir)
		switch {
		case fp != last:
			last, pending = fp, true
		case pending:
			pending = false
			onChange()
		}
	}
}

func fingerprint(dir string) string {
	var b strings.Builder
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			fmt.Fprintf(&b, "%s:%d:%d;", path, info.Size(), info.ModTime().UnixNano())
		}
		return nil
	})
	return b.String()
}
