// Package model contient les types partagés entre la collecte (sources),
// le stockage (store) et l'API HTTP.
package model

import "time"

// StageID identifie une étape de la chaîne de déploiement affichée.
type StageID string

const (
	StageImage   StageID = "image"
	StagePhobos  StageID = "phobos"
	StageCompose StageID = "compose"
	StageGitHub  StageID = "github"
	StageActions StageID = "actions"
	StageACR     StageID = "acr"
	StageCaaS    StageID = "caas"
)

// Status est l'état d'une étape, d'un step de CI ou d'un déploiement.
type Status string

const (
	StatusSuccess Status = "success"
	StatusRunning Status = "running"
	StatusFailed  Status = "failed"
	StatusPending Status = "pending"
	StatusUnknown Status = "unknown"
)

// Field est une ligne clé/valeur du panneau de détails.
type Field struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Mono  bool   `json:"mono,omitempty"`
	Link  string `json:"link,omitempty"`
}

// Step est un step de job GitHub Actions (ou équivalent).
type Step struct {
	Name      string     `json:"name"`
	Status    Status     `json:"status"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	DurationS float64    `json:"duration_s"`
	Log       string     `json:"log,omitempty"`
}

// Count est un compteur de résultat de scan (ex. CRITICAL: 0).
type Count struct {
	Label string `json:"label"`
	Value int    `json:"value"`
	Level string `json:"level"` // critical | high | medium | low | info
}

// Finding est un résultat individuel de scan (CVE, règle Dockle…).
type Finding struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Target   string `json:"target"`
	Title    string `json:"title"`
}

// Scan résume un outil d'analyse (Trivy, Dockle).
type Scan struct {
	Tool     string    `json:"tool"`
	Status   Status    `json:"status"`
	Summary  string    `json:"summary"`
	Counts   []Count   `json:"counts"`
	Findings []Finding `json:"findings,omitempty"`
}

// Stage est un nœud de la pipeline visualisée.
type Stage struct {
	ID        StageID   `json:"id"`
	Label     string    `json:"label"`
	Status    Status    `json:"status"`
	Summary   string    `json:"summary"`
	UpdatedAt time.Time `json:"updated_at"`
	Fields    []Field   `json:"fields"`
	Steps     []Step    `json:"steps,omitempty"`
	Scans     []Scan    `json:"scans,omitempty"`
}

// ProdInfo décrit la version actuellement servie en production.
type ProdInfo struct {
	Tag    string    `json:"tag"`
	Digest string    `json:"digest"`
	Since  time.Time `json:"since"`
}

// RunInfo décrit la release qui parcourt (ou vient de parcourir) la chaîne.
// Le frontend s'en sert pour placer le focus sur l'étape en cours.
type RunInfo struct {
	Number    int       `json:"number"`
	Tag       string    `json:"tag"`
	Status    Status    `json:"status"`
	Stage     StageID   `json:"stage,omitempty"` // étape en cours, ou étape en échec
	StepName  string    `json:"step_name,omitempty"`
	StepsDone int       `json:"steps_done"`
	StepCount int       `json:"step_count"`
	StartedAt time.Time `json:"started_at"`
}

// Snapshot est l'état complet renvoyé par /api/status et poussé sur /api/stream.
type Snapshot struct {
	Environment string    `json:"environment"` // environnement qui sert la page : local | phobos | azure
	Source      string    `json:"source"`      // mock | live
	AppVersion  string    `json:"app_version"`
	Prod        ProdInfo  `json:"prod"`
	Run         *RunInfo  `json:"run,omitempty"`
	Stages      []Stage   `json:"stages"`
	GeneratedAt time.Time `json:"generated_at"`
}

// DeploymentStep correspond à une ligne de la table deployment_steps.
type DeploymentStep struct {
	Name      string    `json:"name"`
	Status    Status    `json:"status"`
	StartedAt time.Time `json:"started_at"`
	DurationS float64   `json:"duration_s"`
}

// Deployment correspond à une ligne de la table deployments.
type Deployment struct {
	ID           int64            `json:"id"`
	Tag          string           `json:"tag"`
	Digest       string           `json:"digest"`
	Status       Status           `json:"status"`
	CVECritical  int              `json:"cve_critical"`
	CVEHigh      int              `json:"cve_high"`
	CVEMedium    int              `json:"cve_medium"`
	DockleStatus string           `json:"dockle_status"`
	Signed       bool             `json:"signed"`
	Target       string           `json:"target"`
	RunURL       string           `json:"run_url"`
	StartedAt    time.Time        `json:"started_at"`
	DurationS    float64          `json:"duration_s"`
	Steps        []DeploymentStep `json:"steps,omitempty"`
}

// Stats agrège l'historique des déploiements.
type Stats struct {
	Total        int            `json:"total"`
	Succeeded    int            `json:"succeeded"`
	Failed       int            `json:"failed"`
	FailureRate  float64        `json:"failure_rate"`   // 0..1
	AvgDurationS float64        `json:"avg_duration_s"` // déploiements réussis uniquement
	SignedRate   float64        `json:"signed_rate"`    // 0..1
	ByTarget     map[string]int `json:"by_target"`
	LastDeployAt *time.Time     `json:"last_deploy_at,omitempty"`
}
