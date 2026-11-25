package model

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	tagRe    = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]{1,40})?$`)
	digestRe = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

const (
	maxSteps     = 50
	maxStepName  = 100
	maxCVE       = 100000
	maxDurationS = 24 * 3600
)

// Validate contrôle un déploiement reçu de l'extérieur (CI) avant insertion.
// Tout ce qui sera ré-affiché dans le navigateur est borné et typé : on refuse
// plutôt que de « nettoyer », et les URL sont limitées à https://github.com
// pour empêcher l'injection d'un lien javascript: dans la timeline.
func (d *Deployment) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if !tagRe.MatchString(d.Tag) {
		add("tag: format semver attendu (ex. v1.2.3)")
	}
	if !digestRe.MatchString(d.Digest) {
		add("digest: format sha256:<64 hex> attendu")
	}
	switch d.Status {
	case StatusSuccess, StatusFailed:
	default:
		add("status: success ou failed attendu")
	}
	switch d.Target {
	case "phobos", "azure":
	default:
		add("target: phobos ou azure attendu")
	}
	switch d.DockleStatus {
	case "pass", "warn", "fatal", "skipped":
	default:
		add("dockle_status: pass, warn, fatal ou skipped attendu")
	}
	for name, v := range map[string]int{"cve_critical": d.CVECritical, "cve_high": d.CVEHigh, "cve_medium": d.CVEMedium} {
		if v < 0 || v > maxCVE {
			add("%s: hors bornes", name)
		}
	}
	if d.DurationS < 0 || d.DurationS > maxDurationS {
		add("duration_s: hors bornes")
	}
	if d.StartedAt.IsZero() || d.StartedAt.After(time.Now().Add(5*time.Minute)) {
		add("started_at: date manquante ou dans le futur")
	}
	if d.RunURL != "" && !isGitHubURL(d.RunURL) {
		add("run_url: doit être une URL https://github.com/…")
	}
	if len(d.Steps) > maxSteps {
		add("steps: %d maximum", maxSteps)
	}
	for i, s := range d.Steps {
		if s.Name == "" || len(s.Name) > maxStepName || strings.ContainsAny(s.Name, "\r\n\x00") {
			add("steps[%d].name: invalide", i)
		}
		switch s.Status {
		case StatusSuccess, StatusFailed, StatusPending:
		default:
			add("steps[%d].status: invalide", i)
		}
		if s.DurationS < 0 || s.DurationS > maxDurationS {
			add("steps[%d].duration_s: hors bornes", i)
		}
	}
	return errors.Join(errs...)
}

func isGitHubURL(raw string) bool {
	if len(raw) > 300 {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host == "github.com" && u.User == nil
}
