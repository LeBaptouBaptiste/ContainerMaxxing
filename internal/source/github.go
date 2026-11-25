package source

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/config"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/github"
	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/model"
)

// GitHubAPI est le sous-ensemble du client GitHub utilisé par la source
// (interface pour pouvoir tester la traduction sans réseau).
type GitHubAPI interface {
	Tags(ctx context.Context) ([]github.Tag, error)
	LatestCommit(ctx context.Context, branch string) (github.Commit, error)
	WorkflowRuns(ctx context.Context, workflow string, n int) ([]github.Run, error)
	RecentRuns(ctx context.Context, n int) ([]github.Run, error)
	Jobs(ctx context.Context, runID int64) ([]github.Job, error)
}

// GitHub est la source « live » : les étapes GitHub et GitHub Actions (et GHCR /
// Container Apps si la CI a des jobs « publish » / « deploy ») viennent de l'API
// GitHub ; les autres étapes ne sont pas encore reliées à des données réelles.
type GitHub struct {
	api      GitHubAPI
	cfg      liveCfg
	version  string
	now      func() time.Time
	workflow string
	branch   string
}

type liveCfg struct {
	env, repo, phobosURL string
}

func NewGitHub(c config.Config, api GitHubAPI, version string) *GitHub {
	return &GitHub{
		api: api, version: version, now: time.Now,
		cfg:      liveCfg{env: c.Environment, repo: c.GitHubRepo, phobosURL: c.PhobosURL},
		workflow: c.GitHubWorkflow, branch: c.GitHubBranch,
	}
}

// Poll construit un snapshot à partir de l'API. En cas d'erreur partielle, les
// étapes concernées passent en « inconnu » et l'erreur est renvoyée pour être
// journalisée : la page reste affichée.
func (g *GitHub) Poll(ctx context.Context) (model.Snapshot, error) {
	now := g.now()
	var errs []error

	commit, err := g.api.LatestCommit(ctx, g.branch)
	if err != nil {
		errs = append(errs, fmt.Errorf("dernier commit: %w", err))
	}
	tags, err := g.api.Tags(ctx)
	if err != nil {
		errs = append(errs, fmt.Errorf("tags: %w", err))
	}
	latestTag := highestSemver(tags)

	runs, err := g.api.WorkflowRuns(ctx, g.workflow, 20)
	ciMissing := errors.Is(err, github.ErrNotFound)
	if err != nil && !ciMissing {
		errs = append(errs, fmt.Errorf("runs %s: %w", g.workflow, err))
	}

	var run *github.Run
	var jobs []github.Job
	if len(runs) > 0 {
		run = &runs[0]
		if jobs, err = g.api.Jobs(ctx, run.ID); err != nil {
			errs = append(errs, fmt.Errorf("jobs du run %d: %w", run.ID, err))
		}
	}

	// Steps de la CI répartis par étape, selon le nom du job.
	byStage := map[model.StageID][]model.Step{}
	for _, j := range jobs {
		id := stageForJob(j.Name)
		byStage[id] = append(byStage[id], jobSteps(j)...)
	}

	snap := model.Snapshot{
		Environment: g.cfg.env,
		Source:      "live",
		AppVersion:  g.version,
		Prod:        g.prodInfo(runs),
		GeneratedAt: now,
	}
	snap.Stages = []model.Stage{
		notConnected(model.StageImage, "Image", now, "données réelles avec la CI (Dev 6)"),
		g.phobosStage(now),
		notConnected(model.StageCompose, "Podman compose", now, "pas encore relié à des données réelles"),
		g.githubStage(now, commit, latestTag, errs),
		g.actionsStage(ctx, now, run, byStage[model.StageActions], ciMissing),
		g.ciStage(model.StageGHCR, "GHCR", now, run, byStage[model.StageGHCR], "données réelles avec le job « publish » (Dev 6)"),
		g.ciStage(model.StageCaaS, "Container Apps", now, run, byStage[model.StageCaaS], "données réelles avec le job « deploy » (Dev 7)"),
	}
	snap.Run = runInfo(run, snap.Stages)
	return snap, errors.Join(errs...)
}

// --- étapes ---------------------------------------------------------------------

func notConnected(id model.StageID, label string, now time.Time, why string) model.Stage {
	return model.Stage{ID: id, Label: label, Status: model.StatusUnknown, UpdatedAt: now, Summary: "non branché · " + why}
}

// phobosStage : si l'app tourne elle-même sur phobos, c'est un fait vérifié.
func (g *GitHub) phobosStage(now time.Time) model.Stage {
	if g.cfg.env != "phobos" {
		return notConnected(model.StagePhobos, "phobos", now, "pas encore relié à des données réelles")
	}
	return model.Stage{
		ID: model.StagePhobos, Label: "phobos", Status: model.StatusSuccess, UpdatedAt: now,
		Summary: "cette page est servie par phobos · " + g.version,
		Fields: []model.Field{
			{Label: "URL", Value: g.cfg.phobosURL, Link: g.cfg.phobosURL},
			{Label: "Version servie", Value: g.version},
			{Label: "Reverse proxy", Value: "Nginx Proxy Manager sur beacon · TLS Let's Encrypt"},
		},
	}
}

func (g *GitHub) githubStage(now time.Time, c github.Commit, tag string, errs []error) model.Stage {
	st := model.Stage{ID: model.StageGitHub, Label: "GitHub", UpdatedAt: now}
	repoURL := "https://github.com/" + g.cfg.repo
	if c.SHA == "" {
		st.Status, st.Summary = model.StatusUnknown, "API GitHub indisponible"
		if len(errs) > 0 {
			st.Fields = []model.Field{{Label: "Erreur", Value: truncate(errs[0].Error(), 120)}}
		}
		return st
	}
	st.Status = model.StatusSuccess
	tagLabel, tagLink := "aucun tag", ""
	if tag != "" {
		tagLabel, tagLink = tag, repoURL+"/releases/tag/"+tag
	}
	st.Summary = fmt.Sprintf("%s · %s", tagLabel, short(c.SHA))
	author := "?"
	if c.Author != nil && c.Author.Login != "" {
		author = c.Author.Login
	}
	st.Fields = []model.Field{
		{Label: "Dépôt", Value: g.cfg.repo, Link: repoURL},
		{Label: "Dernier tag", Value: tagLabel, Link: tagLink},
		{Label: "Branche", Value: g.branch},
		{Label: "Dernier commit", Value: short(c.SHA), Mono: true, Link: c.HTMLURL},
		{Label: "Message", Value: truncate(firstLine(c.Commit.Message), 100)},
		{Label: "Auteur", Value: author},
		{Label: "Date", Value: c.Commit.Author.Date.Local().Format("02/01/2006 15:04")},
	}
	return st
}

func (g *GitHub) actionsStage(ctx context.Context, now time.Time, run *github.Run, steps []model.Step, ciMissing bool) model.Stage {
	st := model.Stage{ID: model.StageActions, Label: "GitHub Actions", UpdatedAt: now}
	if ciMissing || run == nil {
		st.Status = model.StatusUnknown
		st.Summary = fmt.Sprintf("workflow %s pas encore créé", g.workflow)
		if !ciMissing {
			st.Summary = fmt.Sprintf("aucun run de %s", g.workflow)
		}
		// Montre quand même que l'API répond : derniers runs, tous workflows confondus.
		if recent, err := g.api.RecentRuns(ctx, 5); err == nil && len(recent) > 0 {
			for _, r := range recent {
				st.Fields = append(st.Fields, model.Field{
					Label: fmt.Sprintf("#%d", r.RunNumber),
					Value: fmt.Sprintf("%s · %s", truncate(r.Name, 40), runLabel(r)),
					Link:  r.HTMLURL,
				})
			}
		}
		return st
	}
	st.Steps = steps
	st.Status = aggregate(steps, runStatus(*run))
	switch st.Status {
	case model.StatusRunning:
		st.Summary = fmt.Sprintf("run #%d · %s", run.RunNumber, currentStep(steps))
	case model.StatusFailed:
		st.Summary = fmt.Sprintf("run #%d échoué · %s", run.RunNumber, failedStep(steps))
	case model.StatusSuccess:
		st.Summary = fmt.Sprintf("run #%d réussi", run.RunNumber)
	default:
		st.Summary = fmt.Sprintf("run #%d · %s", run.RunNumber, runLabel(*run))
	}
	st.Fields = []model.Field{
		{Label: "Workflow", Value: ".github/workflows/" + g.workflow, Mono: true},
		{Label: "Run", Value: fmt.Sprintf("#%d", run.RunNumber), Link: run.HTMLURL},
		{Label: "Déclencheur", Value: fmt.Sprintf("%s · %s", run.Event, run.HeadBranch)},
		{Label: "Commit", Value: short(run.HeadSHA), Mono: true},
		{Label: "État", Value: runLabel(*run)},
	}
	return st
}

// ciStage : GHCR et Container Apps sont alimentés par les jobs « publish » et
// « deploy » du workflow ; sans ces jobs, l'étape n'est pas encore branchée.
func (g *GitHub) ciStage(id model.StageID, label string, now time.Time, run *github.Run, steps []model.Step, why string) model.Stage {
	if run == nil || len(steps) == 0 {
		return notConnected(id, label, now, why)
	}
	st := model.Stage{ID: id, Label: label, UpdatedAt: now, Steps: steps}
	st.Status = aggregate(steps, model.StatusPending)
	switch st.Status {
	case model.StatusRunning:
		st.Summary = currentStep(steps)
	case model.StatusFailed:
		st.Summary = "échec · " + failedStep(steps)
	case model.StatusSuccess:
		st.Summary = "terminé · run #" + fmt.Sprint(run.RunNumber)
	default:
		st.Summary = "en attente"
	}
	st.Fields = []model.Field{{Label: "Run", Value: fmt.Sprintf("#%d", run.RunNumber), Link: run.HTMLURL}}
	return st
}

// prodInfo : la version en prod est le tag du dernier run réussi de la CI
// déclenché par un tag (le déploiement fait partie de ce run).
func (g *GitHub) prodInfo(runs []github.Run) model.ProdInfo {
	for _, r := range runs {
		if r.Status == "completed" && r.Conclusion == "success" && isSemver(r.HeadBranch) {
			return model.ProdInfo{Tag: r.HeadBranch, Since: r.UpdatedAt}
		}
	}
	return model.ProdInfo{}
}

// runInfo : la release suivie par la caméra est le dernier run de la CI.
func runInfo(run *github.Run, stages []model.Stage) *model.RunInfo {
	if run == nil {
		return nil
	}
	ri := &model.RunInfo{Number: run.RunNumber, Tag: run.HeadBranch, StartedAt: run.RunStartedAt}
	if !isSemver(ri.Tag) {
		ri.Tag = short(run.HeadSHA)
	}
	for _, st := range stages {
		for _, s := range st.Steps {
			ri.StepCount++
			if s.Status == model.StatusSuccess || s.Status == model.StatusSkipped {
				ri.StepsDone++
			}
		}
	}
	ri.Status = model.StatusSuccess
	for _, st := range stages {
		if len(st.Steps) == 0 {
			continue
		}
		switch st.Status {
		case model.StatusRunning, model.StatusPending:
			if ri.Status == model.StatusSuccess {
				ri.Status, ri.Stage, ri.StepName = model.StatusRunning, st.ID, currentStep(st.Steps)
			}
		case model.StatusFailed:
			ri.Status, ri.Stage, ri.StepName = model.StatusFailed, st.ID, failedStep(st.Steps)
			return ri
		}
	}
	if s := runStatus(*run); ri.Status == model.StatusSuccess && s != model.StatusSuccess {
		ri.Status = s // run annulé ou en file d'attente sans step encore visible
	}
	return ri
}

// --- traduction GitHub → modèle ---------------------------------------------------

func stageForJob(name string) model.StageID {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "publish"):
		return model.StageGHCR
	case strings.Contains(n, "deploy"):
		return model.StageCaaS
	default:
		return model.StageActions
	}
}

// jobSteps convertit les steps d'un job, sans les steps techniques ajoutés par GitHub.
func jobSteps(j github.Job) []model.Step {
	var out []model.Step
	for _, s := range j.Steps {
		if s.Name == "Set up job" || s.Name == "Complete job" || strings.HasPrefix(s.Name, "Post ") {
			continue
		}
		st := model.Step{Name: truncate(s.Name, 80), Status: ghStatus(s.Status, s.Conclusion)}
		if s.StartedAt != nil {
			t := *s.StartedAt
			st.StartedAt = &t
			if s.CompletedAt != nil {
				st.DurationS = s.CompletedAt.Sub(t).Seconds()
			}
		}
		out = append(out, st)
	}
	return out
}

func ghStatus(status, conclusion string) model.Status {
	switch status {
	case "in_progress":
		return model.StatusRunning
	case "completed":
		switch conclusion {
		case "success", "neutral":
			return model.StatusSuccess
		case "skipped":
			return model.StatusSkipped
		default: // failure, cancelled, timed_out, action_required, startup_failure
			return model.StatusFailed
		}
	default: // queued, waiting, pending, requested
		return model.StatusPending
	}
}

func runStatus(r github.Run) model.Status { return ghStatus(r.Status, r.Conclusion) }

func aggregate(steps []model.Step, fallback model.Status) model.Status {
	if len(steps) == 0 {
		return fallback
	}
	var running, failed, done int
	for _, s := range steps {
		switch s.Status {
		case model.StatusFailed:
			failed++
		case model.StatusRunning:
			running++
		case model.StatusSuccess, model.StatusSkipped:
			done++
		}
	}
	switch {
	case failed > 0:
		return model.StatusFailed
	case running > 0 || (done > 0 && done < len(steps)):
		return model.StatusRunning
	case done == len(steps):
		return model.StatusSuccess
	default:
		return model.StatusPending
	}
}

func currentStep(steps []model.Step) string {
	for _, s := range steps {
		if s.Status == model.StatusRunning || s.Status == model.StatusPending {
			return s.Name
		}
	}
	return ""
}

func failedStep(steps []model.Step) string {
	for _, s := range steps {
		if s.Status == model.StatusFailed {
			return s.Name
		}
	}
	return ""
}

func runLabel(r github.Run) string {
	if r.Status != "completed" {
		return r.Status
	}
	return r.Conclusion
}

func highestSemver(tags []github.Tag) string {
	best, ba, bb, bc := "", -1, -1, -1
	for _, t := range tags {
		var a, b, c int
		if n, _ := fmt.Sscanf(t.Name, "v%d.%d.%d", &a, &b, &c); n != 3 || !isSemver(t.Name) {
			continue
		}
		if a > ba || (a == ba && (b > bb || (b == bb && c > bc))) {
			best, ba, bb, bc = t.Name, a, b, c
		}
	}
	return best
}

func isSemver(s string) bool {
	var a, b, c int
	n, err := fmt.Sscanf(s, "v%d.%d.%d", &a, &b, &c)
	return err == nil && n == 3 && s == fmt.Sprintf("v%d.%d.%d", a, b, c)
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}

// truncate coupe sur une frontière de caractère (texte venant de l'API, non maîtrisé).
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
