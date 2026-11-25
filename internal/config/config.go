// Package config lit toute la configuration depuis l'environnement.
// Aucune valeur sensible n'est codée en dur ni envoyée au frontend.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

type Config struct {
	ListenAddr   string        // LISTEN_ADDR
	Environment  string        // APP_ENV : local | phobos | azure
	DataSource   string        // DATA_SOURCE : mock (seule valeur à l'étape 1)
	PollInterval time.Duration // POLL_INTERVAL, borné entre 2s et 60s
	IngestToken  string        // INGEST_TOKEN ou INGEST_TOKEN_FILE ; vide = POST désactivé
	LogLevel     string        // LOG_LEVEL : debug | info | warn | error

	// Mode développement : frontend lu sur le disque + rechargement automatique
	// du navigateur. Refusé hors APP_ENV=local.
	DevMode   bool   // DEV_MODE=1
	StaticDir string // STATIC_DIR (requis en mode dev), ex. web/static

	// Base PostgreSQL de l'historique. Vide = stockage en mémoire, accepté
	// uniquement en local (rien ne survit à un redémarrage).
	DatabaseURL string // DATABASE_URL ou DATABASE_URL_FILE (contient le mot de passe)

	// Contrôles de démo (scénarios, pause, pas à pas), disponibles uniquement
	// avec la source mock : sans jeton en local, avec DEMO_TOKEN ailleurs.
	DemoToken string // DEMO_TOKEN ou DEMO_TOKEN_FILE

	// Informations publiques affichées dans les panneaux (pas des secrets).
	GitHubRepo string // GITHUB_REPOSITORY (owner/repo)
	PhobosURL  string // PHOBOS_URL
	AzureURL   string // AZURE_URL
	GHCRImage  string // GHCR_IMAGE (ex. ghcr.io/owner/repo, sans tag)
	CaaSApp    string // CAAS_APP
}

func Load() (Config, error) {
	c := Config{
		ListenAddr:  env("LISTEN_ADDR", ":8080"),
		Environment: env("APP_ENV", "local"),
		DataSource:  env("DATA_SOURCE", "mock"),
		LogLevel:    env("LOG_LEVEL", "info"),
		GitHubRepo:  env("GITHUB_REPOSITORY", "LeBaptouBaptiste/ContainerMaxxing"),
		PhobosURL:   env("PHOBOS_URL", "https://containermaxxing.voyager3.fr"),
		AzureURL:    env("AZURE_URL", "https://cmx.voyager3.fr"),
		CaaSApp:     env("CAAS_APP", "containermaxxing"),
	}
	// GHCR impose des noms en minuscules.
	c.GHCRImage = strings.ToLower(env("GHCR_IMAGE", "ghcr.io/"+c.GitHubRepo))

	var err error
	if c.IngestToken, err = secret("INGEST_TOKEN"); err != nil {
		return c, err
	}
	if c.IngestToken != "" && len(c.IngestToken) < 32 {
		return c, fmt.Errorf("INGEST_TOKEN trop court (32 caractères minimum)")
	}
	if c.DatabaseURL, err = secret("DATABASE_URL"); err != nil {
		return c, err
	}
	if c.DemoToken, err = secret("DEMO_TOKEN"); err != nil {
		return c, err
	}
	if c.DemoToken != "" && len(c.DemoToken) < 32 {
		return c, fmt.Errorf("DEMO_TOKEN trop court (32 caractères minimum)")
	}

	// En mock, un poll = un step : 2 s donne une animation fluide pour la démo.
	defPoll := "5s"
	if c.DataSource == "mock" {
		defPoll = "2s"
	}
	if c.PollInterval, err = time.ParseDuration(env("POLL_INTERVAL", defPoll)); err != nil {
		return c, fmt.Errorf("POLL_INTERVAL: %w", err)
	}
	c.PollInterval = min(max(c.PollInterval, 2*time.Second), 60*time.Second)

	switch c.Environment {
	case "local", "phobos", "azure":
	default:
		return c, fmt.Errorf("APP_ENV invalide: %q", c.Environment)
	}
	if c.DatabaseURL == "" && c.Environment != "local" {
		// Sur un vrai déploiement, perdre l'historique à chaque redémarrage n'est pas acceptable.
		return c, fmt.Errorf("DATABASE_URL requis avec APP_ENV=%s", c.Environment)
	}
	switch c.DataSource {
	case "mock":
	default:
		return c, fmt.Errorf("DATA_SOURCE invalide: %q", c.DataSource)
	}

	c.DevMode = os.Getenv("DEV_MODE") == "1"
	if c.DevMode {
		// Garde-fou : le mode dev sert des fichiers du disque et ajoute des
		// événements de rechargement ; il ne doit jamais tourner en prod.
		if c.Environment != "local" {
			return c, fmt.Errorf("DEV_MODE interdit avec APP_ENV=%s", c.Environment)
		}
		if c.StaticDir = os.Getenv("STATIC_DIR"); c.StaticDir == "" {
			return c, fmt.Errorf("STATIC_DIR requis en mode dev")
		}
	}
	return c, nil
}

// DemoEnabled : les contrôles de démo n'existent qu'avec la source mock, et
// hors poste local seulement si un jeton les protège.
func (c Config) DemoEnabled() bool {
	return c.DataSource == "mock" && (c.Environment == "local" || c.DemoToken != "")
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// secret lit NAME_FILE (fichier monté, ex. secret podman/Docker) en priorité,
// puis NAME. Le fichier évite que la valeur apparaisse dans `podman inspect`.
func secret(name string) (string, error) {
	if path := os.Getenv(name + "_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("%s_FILE: %w", name, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return os.Getenv(name), nil
}
