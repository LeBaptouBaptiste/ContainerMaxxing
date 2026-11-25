-- Historique des déploiements.
-- Les contraintes CHECK doublent la validation de l'API (défense en profondeur) :
-- même une écriture qui contournerait l'application ne peut pas insérer de
-- valeur hors domaine (lien javascript:, statut inconnu, digest invalide…).

CREATE TABLE deployments (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tag           TEXT NOT NULL CHECK (length(tag) BETWEEN 1 AND 64),
    digest        TEXT NOT NULL CHECK (digest ~ '^sha256:[a-f0-9]{64}$'),
    status        TEXT NOT NULL CHECK (status IN ('success', 'failed')),
    cve_critical  INTEGER NOT NULL DEFAULT 0 CHECK (cve_critical >= 0),
    cve_high      INTEGER NOT NULL DEFAULT 0 CHECK (cve_high >= 0),
    cve_medium    INTEGER NOT NULL DEFAULT 0 CHECK (cve_medium >= 0),
    dockle_status TEXT NOT NULL CHECK (dockle_status IN ('pass', 'warn', 'fatal', 'skipped')),
    signed        BOOLEAN NOT NULL DEFAULT false,
    target        TEXT NOT NULL CHECK (target IN ('phobos', 'azure')),
    run_url       TEXT NOT NULL DEFAULT '' CHECK (run_url = '' OR run_url LIKE 'https://github.com/%'),
    started_at    TIMESTAMPTZ NOT NULL,
    duration_s    DOUBLE PRECISION NOT NULL CHECK (duration_s >= 0)
);

-- La timeline et les stats lisent les déploiements du plus récent au plus ancien.
CREATE INDEX deployments_started_at_idx ON deployments (started_at DESC, id DESC);

CREATE TABLE deployment_steps (
    deployment_id BIGINT NOT NULL REFERENCES deployments (id) ON DELETE CASCADE,
    position      SMALLINT NOT NULL CHECK (position >= 0), -- ordre d'exécution des steps
    name          TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    status        TEXT NOT NULL CHECK (status IN ('success', 'failed', 'pending', 'skipped')),
    started_at    TIMESTAMPTZ NOT NULL,
    duration_s    DOUBLE PRECISION NOT NULL CHECK (duration_s >= 0),
    PRIMARY KEY (deployment_id, position)
);
