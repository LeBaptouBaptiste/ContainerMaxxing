package source

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/github"
)

// Extraits de journaux CI affichés dans le panneau « Journal ».
//
// GitHub publie le journal d'un job en un seul bloc texte, et seulement à la fin
// du job. Chaque ligne commence par un horodatage à la 100 ns ; chaque step
// commence par une ligne « ##[group]Run … ». Les steps de l'API ne sont datés qu'à
// la seconde, d'où le découpage : la frontière du step k est la première ligne
// « ##[group]Run » postérieure à celle du step k-1 et datée au plus tôt de la
// seconde de début du step k. (Une action composite ouvre plusieurs groupes
// « Run » : ses groupes internes restent dans son propre extrait, sauf s'ils
// tombent dans la même seconde que le début du step suivant.)

const (
	logTailLines = 15  // lignes gardées par step (les dernières)
	logLineRunes = 200 // longueur max d'une ligne

	maxStepLogBytes = 4 << 20 // fichier de step décompressé (garde-fou anti-bombe zip)
)

var (
	ansiRe    = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)
	spinnerRe = regexp.MustCompile(`^[/|\\-] Running \.\.$`) // indicateur de progression d'az
)

type logLine struct {
	ts   time.Time
	text string
}

// splitJobLog renvoie, par numéro de step, la fin nettoyée de sa sortie.
func splitJobLog(raw string, steps []github.Step) map[int]string {
	lines := parseLogLines(raw)

	var mapped []github.Step
	for _, s := range steps {
		if !hasOutput(s) {
			continue
		}
		mapped = append(mapped, s)
	}

	// Frontières : index de la ligne « ##[group]Run » ouvrant chaque step.
	bounds := make([]int, 0, len(mapped))
	from := 0
	for _, s := range mapped {
		start := s.StartedAt.Truncate(time.Second)
		found := -1
		for i := from; i < len(lines); i++ {
			if strings.HasPrefix(lines[i].text, "##[group]Run ") && !lines[i].ts.Before(start) {
				found = i
				break
			}
		}
		if found < 0 {
			break // steps suivants sans extrait plutôt qu'un extrait faux
		}
		bounds = append(bounds, found)
		from = found + 1
	}

	out := map[int]string{}
	for k, b := range bounds {
		end := len(lines)
		if k+1 < len(bounds) {
			end = bounds[k+1]
		}
		if tail := cleanSegment(lines[b:end]); tail != "" {
			out[mapped[k].Number] = tail
		}
	}
	return out
}

// hasOutput : steps qui écrivent une section « Run » dans le journal.
func hasOutput(s github.Step) bool {
	if s.StartedAt == nil || s.Status != "completed" || s.Conclusion == "skipped" {
		return false
	}
	return !isTechnicalStep(s.Name)
}

func isTechnicalStep(name string) bool {
	return name == "Set up job" || name == "Complete job" || strings.HasPrefix(name, "Post ")
}

func parseLogLines(raw string) []logLine {
	raw = strings.TrimPrefix(raw, "\uFEFF")
	raw = strings.ToValidUTF8(raw, "�")
	var out []logLine
	var last time.Time
	for _, l := range strings.Split(raw, "\n") {
		l = strings.TrimRight(l, "\r")
		if i := strings.IndexByte(l, ' '); i > 0 && i <= 40 {
			if t, err := time.Parse(time.RFC3339Nano, l[:i]); err == nil {
				last, l = t, l[i+1:]
			}
		}
		out = append(out, logLine{ts: last, text: l})
	}
	return out
}

// cleanSegment retire l'en-tête du step (script et variables d'environnement,
// dans le premier groupe), le nettoyage de fin de job, les codes ANSI et les
// commandes internes du runner, puis garde les dernières lignes.
func cleanSegment(seg []logLine) string {
	var kept []string
	depth := 0
	for _, l := range seg {
		t := ansiRe.ReplaceAllString(l.text, "")
		switch {
		case t == "Post job cleanup." || t == "Evaluate and set job outputs" || t == "Cleaning up orphan processes":
			return tail(kept)
		case strings.HasPrefix(t, "##[group]"):
			depth++
			continue
		case strings.HasPrefix(t, "##[endgroup]"):
			if depth > 0 {
				depth--
			}
			continue
		case depth > 0:
			continue
		case strings.HasPrefix(t, "##[error]"):
			t = "error: " + strings.TrimPrefix(t, "##[error]")
		case strings.HasPrefix(t, "##[warning]"):
			t = "warning: " + strings.TrimPrefix(t, "##[warning]")
		case strings.HasPrefix(t, "##["):
			continue // debug, notice, commandes internes
		case strings.HasPrefix(t, "[command]"):
			t = "$ " + strings.TrimPrefix(t, "[command]")
		}
		if strings.TrimSpace(t) == "" || spinnerRe.MatchString(t) {
			continue
		}
		kept = append(kept, truncate(t, logLineRunes))
	}
	return tail(kept)
}

func tail(lines []string) string {
	if len(lines) > logTailLines {
		lines = lines[len(lines)-logTailLines:]
	}
	return strings.Join(lines, "\n")
}

// jobLogCache garde les extraits du run affiché, téléchargés une seule fois :
//   - pendant le run, le journal de chaque job terminé, découpé par horodatage
//     (approché si une action composite finit dans la même seconde que le step
//     suivant) ;
//   - une fois le run terminé, l'archive du run, qui contient un fichier par
//     step : les extraits deviennent exacts et ne sont plus redemandés.
type jobLogCache struct {
	mu    sync.Mutex
	runID int64
	exact bool // extraits tirés de l'archive du run (définitifs)
	byID  map[int64]map[int]string
}

// logsFor renvoie les extraits par numéro de step pour chaque job terminé du run.
// Un journal pas encore publié (404) est redemandé au poll suivant.
func (c *jobLogCache) logsFor(ctx context.Context, api GitHubAPI, run *github.Run, jobs []github.Job) (map[int64]map[int]string, error) {
	if run == nil {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if run.ID != c.runID || c.byID == nil {
		c.runID, c.exact, c.byID = run.ID, false, map[int64]map[int]string{}
	}
	var errs []error

	if !c.exact && run.Status == "completed" {
		raw, err := api.RunLogs(ctx, run.ID)
		switch {
		case err == nil:
			perJob, perr := parseRunArchive(raw)
			if perr != nil {
				errs = append(errs, perr)
				break
			}
			for _, j := range jobs {
				if m, ok := perJob[j.Name]; ok {
					c.byID[j.ID] = m
				}
			}
			c.exact = true
		case !errors.Is(err, github.ErrNotFound):
			errs = append(errs, err)
		}
	}

	for _, j := range jobs {
		if j.Status != "completed" || j.Conclusion == "skipped" {
			continue
		}
		if _, ok := c.byID[j.ID]; ok {
			continue
		}
		raw, err := api.JobLogs(ctx, j.ID)
		if err != nil {
			if !errors.Is(err, github.ErrNotFound) {
				errs = append(errs, err)
			}
			continue
		}
		c.byID[j.ID] = splitJobLog(raw, j.Steps)
	}
	return c.byID, errors.Join(errs...)
}

// parseRunArchive lit l'archive zip des journaux d'un run : un dossier par job
// (nommé comme le job), un fichier « <numéro>_<nom du step>.txt » par step.
func parseRunArchive(raw []byte) (map[string]map[int]string, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("archive des journaux: %w", err)
	}
	out := map[string]map[int]string{}
	for _, f := range zr.File {
		dir, file := path.Split(f.Name)
		job := strings.TrimSuffix(dir, "/")
		num, _, ok := strings.Cut(file, "_")
		n, err := strconv.Atoi(num)
		if job == "" || !ok || err != nil || f.UncompressedSize64 > maxStepLogBytes {
			continue // journal complet du job, system.txt, ou fichier anormal
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(rc, maxStepLogBytes))
		rc.Close()
		if err != nil {
			continue
		}
		if tail := cleanSegment(parseLogLines(string(b))); tail != "" {
			if out[job] == nil {
				out[job] = map[int]string{}
			}
			out[job][n] = tail
		}
	}
	return out, nil
}
