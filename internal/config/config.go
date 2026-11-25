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

	// Informations publiques affichées dans les panneaux (pas des secrets).
	GitHubRepo string // GITHUB_REPOSITORY (owner/repo)
	PhobosURL  string // PHOBOS_URL
	AzureURL   string // AZURE_URL
	ACRName    string // ACR_NAME
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
		ACRName:     env("ACR_NAME", "containermaxxing"),
		CaaSApp:     env("CAAS_APP", "containermaxxing"),
	}

	var err error
	if c.IngestToken, err = secret("INGEST_TOKEN"); err != nil {
		return c, err
	}
	if c.IngestToken != "" && len(c.IngestToken) < 32 {
		return c, fmt.Errorf("INGEST_TOKEN trop court (32 caractères minimum)")
	}

	if c.PollInterval, err = time.ParseDuration(env("POLL_INTERVAL", "5s")); err != nil {
		return c, fmt.Errorf("POLL_INTERVAL: %w", err)
	}
	c.PollInterval = min(max(c.PollInterval, 2*time.Second), 60*time.Second)

	switch c.Environment {
	case "local", "phobos", "azure":
	default:
		return c, fmt.Errorf("APP_ENV invalide: %q", c.Environment)
	}
	switch c.DataSource {
	case "mock":
	default:
		return c, fmt.Errorf("DATA_SOURCE invalide: %q", c.DataSource)
	}
	return c, nil
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
