// Package github interroge l'API REST de GitHub (lecture seule) pour alimenter
// les étapes « GitHub » et « GitHub Actions » de la chaîne.
//
// Trois protections pour respecter le quota d'API (5 000 requêtes/h avec jeton,
// 60/h sans) malgré un polling toutes les quelques secondes :
//   - cache partagé (Redis via cache.Loader) avec une durée de vie par ressource ;
//   - requêtes conditionnelles (If-None-Match) : une réponse 304 ne consomme pas
//     de quota et réutilise le corps déjà reçu ;
//   - respect des limites : après un 403/429 de quota, plus aucune requête jusqu'à
//     l'heure de réinitialisation annoncée par GitHub.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/cache"
)

const (
	DefaultBaseURL = "https://api.github.com"
	apiVersion     = "2022-11-28"
	maxBodyBytes   = 2 << 20 // une réponse plus grosse est refusée
	notFoundTTL    = 5 * time.Minute
)

var (
	// ErrNotFound : la ressource n'existe pas (ex. workflow pas encore créé).
	ErrNotFound = errors.New("github: ressource introuvable")
	// ErrRateLimited : quota épuisé, aucune requête avant la réinitialisation.
	ErrRateLimited = errors.New("github: quota d'API épuisé")

	repoRe     = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}/[A-Za-z0-9._-]{1,100}$`)
	workflowRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}\.ya?ml$`)
	branchRe   = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,100}$`)
)

type Options struct {
	BaseURL    string // défaut : https://api.github.com
	Repo       string // owner/repo
	Token      string // optionnel ; jeton en lecture seule recommandé (quota de 5 000/h)
	Loader     *cache.Loader
	Log        *slog.Logger
	HTTPClient *http.Client
}

// Client est sûr pour un usage concurrent.
type Client struct {
	base, repo, token string
	hc                *http.Client
	loader            *cache.Loader
	log               *slog.Logger
	now               func() time.Time

	mu           sync.Mutex
	etags        map[string]stored    // path → dernière réponse 200 (ETag + corps)
	missing      map[string]time.Time // path → 404 mémorisé jusqu'à cette date
	blockedUntil time.Time
	remaining    int
}

type stored struct {
	etag string
	body []byte
}

func New(o Options) (*Client, error) {
	if !repoRe.MatchString(o.Repo) {
		return nil, fmt.Errorf("github: dépôt invalide %q (attendu owner/repo)", o.Repo)
	}
	if o.BaseURL == "" {
		o.BaseURL = DefaultBaseURL
	}
	u, err := url.Parse(o.BaseURL)
	if err != nil || (u.Scheme != "https" && u.Hostname() != "127.0.0.1") {
		// HTTP en clair seulement vers la boucle locale (tests) : le jeton ne doit jamais circuler en clair.
		return nil, fmt.Errorf("github: URL d'API invalide %q", o.BaseURL)
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{
		base: o.BaseURL, repo: o.Repo, token: o.Token, hc: o.HTTPClient,
		loader: o.Loader, log: o.Log, now: time.Now,
		etags: map[string]stored{}, missing: map[string]time.Time{}, remaining: -1,
	}, nil
}

// RateRemaining renvoie le quota restant annoncé par la dernière réponse (-1 si inconnu).
func (c *Client) RateRemaining() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.remaining
}

// getJSON lit path (via le cache partagé) et décode le JSON dans out.
func (c *Client) getJSON(ctx context.Context, path string, ttl time.Duration, out any) error {
	b, err := c.loader.Get(ctx, "github:"+c.repo+":"+path, ttl, func(ctx context.Context) ([]byte, error) {
		return c.fetch(ctx, path)
	})
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func (c *Client) fetch(ctx context.Context, path string) ([]byte, error) {
	c.mu.Lock()
	if until := c.missing[path]; c.now().Before(until) {
		c.mu.Unlock()
		return nil, ErrNotFound
	}
	if c.now().Before(c.blockedUntil) {
		c.mu.Unlock()
		return nil, ErrRateLimited
	}
	prev, hasPrev := c.etags[path]
	c.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	req.Header.Set("User-Agent", "containermaxxing")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if hasPrev && prev.etag != "" {
		req.Header.Set("If-None-Match", prev.etag)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: %w", err)
	}
	defer resp.Body.Close()
	c.trackRate(resp)

	switch {
	case resp.StatusCode == http.StatusNotModified && hasPrev:
		return prev.body, nil
	case resp.StatusCode == http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
		if err != nil {
			return nil, fmt.Errorf("github: lecture de la réponse: %w", err)
		}
		if len(body) > maxBodyBytes {
			return nil, errors.New("github: réponse trop volumineuse")
		}
		c.mu.Lock()
		c.etags[path] = stored{etag: resp.Header.Get("ETag"), body: body}
		c.mu.Unlock()
		return body, nil
	case resp.StatusCode == http.StatusNotFound:
		c.mu.Lock()
		c.missing[path] = c.now().Add(notFoundTTL) // évite de redemander à chaque poll
		c.mu.Unlock()
		return nil, ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests ||
		(resp.StatusCode == http.StatusForbidden && resp.Header.Get("X-RateLimit-Remaining") == "0"):
		c.block(resp)
		return nil, ErrRateLimited
	default:
		return nil, fmt.Errorf("github: %s: HTTP %d", path, resp.StatusCode)
	}
}

func (c *Client) trackRate(resp *http.Response) {
	if v := resp.Header.Get("X-RateLimit-Remaining"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.mu.Lock()
			c.remaining = n
			c.mu.Unlock()
		}
	}
}

// block suspend les requêtes jusqu'à la réinitialisation annoncée par GitHub
// (Retry-After en secondes, ou X-RateLimit-Reset en horodatage Unix).
func (c *Client) block(resp *http.Response) {
	until := c.now().Add(time.Minute)
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
		until = c.now().Add(time.Duration(s) * time.Second)
	} else if r, err := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil {
		until = time.Unix(r, 0)
	}
	c.mu.Lock()
	c.blockedUntil = until
	c.mu.Unlock()
	c.log.Warn("quota d'API GitHub atteint, requêtes suspendues", "jusqu_a", until.Format(time.RFC3339))
}

// --- ressources ------------------------------------------------------------------

type Tag struct {
	Name   string `json:"name"`
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

// Tags renvoie les tags du dépôt (au plus 100).
func (c *Client) Tags(ctx context.Context) ([]Tag, error) {
	var out []Tag
	err := c.getJSON(ctx, "/repos/"+c.repo+"/tags?per_page=100", 60*time.Second, &out)
	return out, err
}

type Commit struct {
	SHA     string `json:"sha"`
	HTMLURL string `json:"html_url"`
	Commit  struct {
		Message string `json:"message"`
		Author  struct {
			Date time.Time `json:"date"`
		} `json:"author"`
	} `json:"commit"`
	Author *struct {
		Login string `json:"login"`
	} `json:"author"`
}

// LatestCommit renvoie le dernier commit de la branche.
func (c *Client) LatestCommit(ctx context.Context, branch string) (Commit, error) {
	if !branchRe.MatchString(branch) {
		return Commit{}, fmt.Errorf("github: branche invalide %q", branch)
	}
	var out []Commit
	err := c.getJSON(ctx, "/repos/"+c.repo+"/commits?sha="+url.QueryEscape(branch)+"&per_page=1", 30*time.Second, &out)
	if err != nil {
		return Commit{}, err
	}
	if len(out) == 0 {
		return Commit{}, ErrNotFound
	}
	return out[0], nil
}

type Run struct {
	ID           int64     `json:"id"`
	RunNumber    int       `json:"run_number"`
	Name         string    `json:"name"`
	Event        string    `json:"event"`
	HeadBranch   string    `json:"head_branch"`
	HeadSHA      string    `json:"head_sha"`
	Status       string    `json:"status"`     // queued | in_progress | completed | waiting | requested | pending
	Conclusion   string    `json:"conclusion"` // success | failure | cancelled | skipped | timed_out | action_required | neutral
	HTMLURL      string    `json:"html_url"`
	Path         string    `json:"path"`
	CreatedAt    time.Time `json:"created_at"`
	RunStartedAt time.Time `json:"run_started_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type runsPage struct {
	WorkflowRuns []Run `json:"workflow_runs"`
}

// WorkflowRuns renvoie les derniers runs d'un workflow (fichier, ex. ci.yml).
// ErrNotFound si le workflow n'existe pas encore.
func (c *Client) WorkflowRuns(ctx context.Context, workflow string, n int) ([]Run, error) {
	if !workflowRe.MatchString(workflow) {
		return nil, fmt.Errorf("github: workflow invalide %q", workflow)
	}
	var page runsPage
	err := c.getJSON(ctx, fmt.Sprintf("/repos/%s/actions/workflows/%s/runs?per_page=%d", c.repo, workflow, clamp(n)), 10*time.Second, &page)
	return page.WorkflowRuns, err
}

// RecentRuns renvoie les derniers runs du dépôt, tous workflows confondus.
func (c *Client) RecentRuns(ctx context.Context, n int) ([]Run, error) {
	var page runsPage
	err := c.getJSON(ctx, fmt.Sprintf("/repos/%s/actions/runs?per_page=%d", c.repo, clamp(n)), 15*time.Second, &page)
	return page.WorkflowRuns, err
}

type Step struct {
	Name        string     `json:"name"`
	Number      int        `json:"number"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

type Job struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	HTMLURL     string     `json:"html_url"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	Steps       []Step     `json:"steps"`
}

// Jobs renvoie les jobs d'un run et leurs steps.
func (c *Client) Jobs(ctx context.Context, runID int64) ([]Job, error) {
	var page struct {
		Jobs []Job `json:"jobs"`
	}
	err := c.getJSON(ctx, fmt.Sprintf("/repos/%s/actions/runs/%d/jobs?per_page=100", c.repo, runID), 5*time.Second, &page)
	return page.Jobs, err
}

func clamp(n int) int { return min(max(n, 1), 100) }
