# ContainerMaxxing

Projet DevSecOps (Sup de Vinci) : une webapp qui **visualise en direct sa propre chaîne de déploiement**,
de l'image locale jusqu'à Azure Container Apps, pour la démo étape par étape en soutenance.

```
Image → phobos → Podman compose → GitHub → GitHub Actions → ACR → Container Apps
└──── déploiement statique (phobos) ────┘ └────────── déploiement continu (Azure) ──────────┘
```

## Avancement

| # | Étape | État |
|---|-------|------|
| 1 | App Go + frontend avec données mockées | ✅ |
| 2 | Dockerfile distroless + scans locaux (Trivy, Dockle) | ⏳ |
| 3 | compose : Postgres, Redis, Caddy, durcissement | ⏳ |
| 4 | Déploiement statique sur phobos (podman-compose + Nginx Proxy Manager) | ⏳ |
| 5 | Branchement GitHub API | ⏳ |
| 6 | Workflow CI (build → Trivy → Dockle → ACR → cosign → CaaS) | ⏳ |
| 7 | Déploiement Azure + branchement ACR / Container Apps | ⏳ |
| 8 | Finitions et mode démo | ⏳ |

## Architecture

```
cmd/server/          point d'entrée (serveur + sous-commande `healthcheck`)
internal/config/     configuration 100 % variables d'environnement
internal/model/      types partagés + validation des entrées
internal/source/     collecte de l'état (Mock aujourd'hui ; GitHub/ACR/CaaS ensuite)
internal/live/       polling périodique + diffusion SSE (hub)
internal/store/      historique des déploiements (mémoire aujourd'hui ; PostgreSQL à l'étape 3)
internal/api/        routes HTTP, middlewares de sécurité
web/static/          frontend HTML/CSS/JS sans dépendance, embarqué dans le binaire (go:embed)
  ├─ chain.js        chaîne animée : piste, nœuds, paquet, spotlight, focus automatique
  ├─ app.js          bandeau, flux SSE, panneau de détails, historique, comparaison
  └─ dom.js          helpers DOM partagés (création sûre d'éléments, icônes, formats)
```

### La chaîne animée

C'est l'élément central de la page. Une release est représentée par un **paquet** (l'image et son tag) qui
avance sur une piste de 7 nœuds. Chaque nœud a un anneau qui se remplit au fil des steps de l'étape, et un
**spotlight** sous la piste détaille l'étape mise en avant : ses steps avec chronomètre, un journal façon
terminal et les infos clés (scans Trivy/Dockle, digest, signature…).

Le focus change tout seul, selon trois modes :

| Mode | Quand | Comportement |
|------|-------|--------------|
| suivi en direct | une release est en cours ou vient d'échouer | le focus suit l'étape active (`run.stage` du snapshot) |
| visite automatique | rien ne tourne | les étapes défilent toutes les 5 s (barre de progression sous le spotlight) |
| sélection manuelle | clic sur un nœud, ◀ ▶ ou flèches du clavier | le focus est figé, puis le suivi auto reprend après 20 s |

Le bouton « Suivi auto » met le défilement en pause. Les animations sont désactivées si le système demande
`prefers-reduced-motion`.

Le serveur **interroge les sources lui-même** (polling toutes les 5 s par défaut) et pousse le dernier état aux
navigateurs via Server-Sent Events. Le navigateur ne parle jamais à GitHub ni à Azure.

### API

| Méthode | Route | Rôle |
|---------|-------|------|
| GET | `/api/status` | état courant des 7 étapes, tag en prod, environnement servant la page |
| GET | `/api/stream` | flux SSE : événements `status` (snapshot) et `deployment` (nouvel enregistrement) |
| GET | `/api/deployments?limit=N` | historique, plus récent d'abord (1 ≤ N ≤ 200) |
| GET | `/api/deployments/{id}` | un déploiement avec ses steps |
| GET | `/api/stats` | total, taux d'échec, durée moyenne, taux de signature |
| POST | `/api/deployments` | enregistrement par la CI en fin de run (jeton Bearer) |
| GET | `/healthz` | sonde de santé |

### Mode mock (étape 1)

`DATA_SOURCE=mock` simule une chaîne vivante. Un historique de 11 déploiements est pré-chargé, puis des releases
successives parcourent **les 7 étapes dans l'ordre** (17 steps, un par poll ; `POLL_INTERVAL` vaut 2 s par
défaut en mock, soit ~45 s par release, suivies de 30 s de pause). Chaque release enregistre un déploiement
`phobos` (après les healthchecks) et un déploiement `azure` (fin de CI).

**Une release sur quatre échoue au scan Trivy de la CI** (vulnérabilité fictive `MOCK-CVE-0001`, absente au scan
local) : le focus reste 20 s sur l'échec, et l'ACR et Container Apps restent sur la version précédente, ce qui
illustre la *quality gate*.

## Lancer en local

Go n'est pas nécessaire sur le poste, Docker/Podman suffit :

```sh
docker run --rm -p 127.0.0.1:8080:8080 -v "$PWD:/src" -w /src golang:1.25-alpine go run ./cmd/server
# puis http://localhost:8080

# tests
docker run --rm -v "$PWD:/src" -w /src golang:1.25-alpine go test ./...
```

Avec Go installé : `go run ./cmd/server` et `go test ./...`. Variables disponibles : voir [`.env.example`](.env.example).

Tester l'ingestion :

```sh
export INGEST_TOKEN=$(openssl rand -hex 32)   # à passer aussi au serveur
curl -X POST http://localhost:8080/api/deployments \
  -H "Authorization: Bearer $INGEST_TOKEN" -H "Content-Type: application/json" \
  -d '{"tag":"v2.0.0","digest":"sha256:<64 hex>","status":"success","cve_critical":0,"cve_high":0,
       "cve_medium":1,"dockle_status":"pass","signed":true,"target":"azure",
       "run_url":"https://github.com/LeBaptouBaptiste/ContainerMaxxing/actions/runs/1",
       "started_at":"2026-11-25T10:00:00Z","duration_s":180}'
```

## Choix de sécurité

### Étape 1 — application

| Choix | Pourquoi |
|-------|----------|
| **Go, binaire unique, bibliothèque standard uniquement** | Aucune dépendance tierce à cette étape → surface de chaîne d'approvisionnement minimale. Le binaire statique (`CGO_ENABLED=0`) tournera dans une image *distroless* sans libc ni shell. |
| **Frontend sans framework ni CDN, embarqué via `go:embed`** | Pas de `node_modules` à auditer, pas de script tiers chargé à l'exécution, rien à monter à côté du binaire. Toute la page vient de la même origine. |
| **Polling côté serveur** | Les jetons GitHub/Azure restent sur le serveur ; le frontend ne reçoit que des données publiques déjà filtrées. Le cache Redis (étape 3) évitera de dépasser les quotas d'API quel que soit le nombre de visiteurs. |
| **Aucun secret dans le code ni dans le frontend** | Toute la configuration passe par l'environnement. Les secrets supportent la variante `*_FILE` (ex. `INGEST_TOKEN_FILE`) pour être montés comme fichiers plutôt qu'exposés dans `podman inspect`. |
| **Content-Security-Policy stricte** | `default-src 'self'`, pas de script ni de style inline, `frame-ancestors 'none'` (anti-clickjacking), `object-src 'none'`, `base-uri 'none'`. Complétée par `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `Permissions-Policy`, COOP/CORP. |
| **Pas de `innerHTML` côté client** | Toutes les données sont insérées via `textContent`/`createElement` : une valeur malveillante en base reste du texte (pas de XSS stockée). Les liens ne sont rendus que s'ils sont en `https:`. |
| **Ingestion authentifiée** | `POST /api/deployments` exige un jeton Bearer d'au moins 32 caractères ; sans jeton configuré, l'endpoint est désactivé (403). Comparaison à temps constant sur les empreintes SHA-256 (pas de fuite par *timing*, même sur la longueur). |
| **Validation stricte des entrées** | Corps limité à 64 Kio, champs inconnus refusés, tag semver, digest `sha256:` sur 64 hex, cibles et statuts en liste blanche, compteurs et durées bornés, `run_url` limitée à `https://github.com/…` (empêche un lien `javascript:` dans la timeline). |
| **Timeouts HTTP** | `ReadHeaderTimeout` (anti-Slowloris), `ReadTimeout`, `WriteTimeout`, `IdleTimeout`, en-têtes limités à 16 Kio. Le flux SSE repousse sa propre échéance à chaque écriture pour rester borné. |
| **Flux SSE limités** | 100 clients maximum (503 + `Retry-After` au-delà) ; diffusion non bloquante : un client lent ne ralentit pas les autres. |
| **Erreurs et logs maîtrisés** | Le client reçoit un message générique, le détail reste dans les logs JSON. Pas de query string ni d'en-têtes (jetons) dans les logs. Récupération des panics. |
| **Arrêt propre sur SIGTERM** | Indispensable en conteneur : les connexions en cours se terminent avant l'arrêt (rolling update de Container Apps, `podman-compose down`). |
| **Sous-commande `healthcheck`** | L'image distroless n'aura ni `curl` ni shell : le binaire sait vérifier lui-même `/healthz` pour le `HEALTHCHECK`. |
