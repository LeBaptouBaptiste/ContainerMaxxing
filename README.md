# ContainerMaxxing

Projet DevSecOps (Sup de Vinci) : une webapp qui **visualise en direct sa propre chaîne de déploiement**,
de l'image locale jusqu'à Azure Container Apps, pour la démo étape par étape en soutenance.

```
Image → phobos → Podman compose → GitHub → GitHub Actions → GHCR → Container Apps
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
| 6 | Workflow CI (build → Trivy → Dockle → GHCR (ghcr.io) → cosign → CaaS) | ⏳ |
| 7 | Déploiement Azure + branchement GHCR / Container Apps | ⏳ |
| 8 | Finitions et mode démo | ⏳ |

## Architecture

```
cmd/server/          point d'entrée (serveur + sous-commande `healthcheck`)
internal/config/     configuration 100 % variables d'environnement
internal/model/      types partagés + validation des entrées
internal/source/     collecte de l'état (Mock aujourd'hui ; GitHub/GHCR/CaaS ensuite)
internal/live/       polling périodique + diffusion SSE (hub)
internal/store/      historique des déploiements (mémoire aujourd'hui ; PostgreSQL à l'étape 3)
internal/api/        routes HTTP, middlewares de sécurité
web/src/             frontend en TypeScript strict, sans framework ni bundler
  ├─ chain.ts        chaîne animée : piste, nœuds, paquet, spotlight, focus automatique
  ├─ app.ts          bandeau, flux SSE, panneau de détails, historique, comparaison
  ├─ dom.ts          helpers DOM partagés (création sûre d'éléments, icônes, formats)
  └─ types.ts        types des réponses de l'API, alignés sur internal/model
web/static/          index.html + app.css ; js/ = sortie de tsc (non commitée)
                     le tout embarqué dans le binaire (go:embed)
scripts/             boucles de rechargement à chaud de l'environnement de dev
compose.dev.yaml     environnement de dev (jamais en prod)
```

Le frontend est compilé par `tsc` (TypeScript 7), seule dépendance npm, et uniquement en dépendance de dev : le
navigateur ne reçoit que le JS compilé, aucun code tiers. **Le build doit donc compiler le TypeScript avant
`go build`**, sinon `web/static/js/` est vide et la page ne s'affiche pas :

```sh
npm ci && npm run build   # web/src/*.ts → web/static/js/*.js
go build ./cmd/server     # embarque web/static/ dans le binaire
```

Pour le Dockerfile, il faut donc un stage `node` (`npm ci && npm run build`) dont on copie `web/static/js` dans
le stage Go avant la compilation.

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

### Mode démo et présentateur

Avec la source mock, une barre de démo apparaît au-dessus de la chaîne. Elle permet de **lancer une release
qui illustre un contrôle de sécurité précis**, chacun bloquant à une étape différente (défense en profondeur) :

| Touche | Scénario | Contrôle qui bloque | Où |
|--------|----------|---------------------|----|
| `1` | Release saine | aucun : la version arrive en prod | — |
| `2` | CVE critique | Trivy (`--exit-code 1` sur HIGH/CRITICAL) | GitHub Actions |
| `3` | Conteneur root | Dockle (`CIS-DI-0001`, `--exit-level warn`) | Image, avant même phobos |
| `4` | Image non signée | `cosign verify` avant le déploiement | Container Apps |

Dans les trois cas d'échec, la prod reste sur la version précédente.

| Touche | Action |
|--------|--------|
| `P` | pause / lecture de la release (côté serveur : tous les écrans ouverts suivent) |
| `Espace` | termine le step en cours, pour commenter chaque étape à son rythme |
| `Maj` + `Espace` | revient sur un état précédent de la release (rejeu local, ne change rien côté serveur) |
| `F` | mode présentation : chaîne en pleine largeur, texte agrandi, historique masqué |
| `←` `→` | change l'étape mise en avant |

Une release terminée n'est jamais relancée par `Espace` : un appui de trop ne doit pas effacer l'écran d'échec
pendant qu'on l'explique. On lance la suivante volontairement avec `1`–`4`.

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
| POST | `/api/demo/release` | lance un scénario `{"scenario":"cve"}` (mode démo uniquement) |
| POST | `/api/demo/pause` · `/play` · `/step` | pilotage de la release (mode démo uniquement) |

### Mode mock (étape 1)

`DATA_SOURCE=mock` simule une chaîne vivante. Un historique de 11 déploiements est pré-chargé, puis des releases
successives parcourent **les 7 étapes dans l'ordre** (17 steps, un par poll ; `POLL_INTERVAL` vaut 2 s par
défaut en mock, soit ~45 s par release, suivies de 30 s de pause). Chaque release enregistre un déploiement
`phobos` (après les healthchecks) et un déploiement `azure` (fin de CI).

**Une release sur quatre échoue au scan Trivy de la CI** (vulnérabilité fictive `MOCK-CVE-0001`, absente au scan
local) : le focus reste 20 s sur l'échec, et GHCR et Container Apps restent sur la version précédente, ce qui
illustre la *quality gate*.

## Développement

Ni Go ni Node ne sont nécessaires sur le poste, Docker suffit. L'environnement de dev reste lancé en
permanence et **recharge le navigateur tout seul** à chaque modification :

```sh
docker compose -f compose.dev.yaml up -d      # puis http://localhost:8080
docker compose -f compose.dev.yaml logs -f    # suivre les recompilations
docker compose -f compose.dev.yaml down       # arrêter
```

| Modification | Ce qui se passe |
|--------------|-----------------|
| `web/src/*.ts` | le conteneur `web` recompile (`tsc`), le serveur voit le nouveau JS et envoie `reload` aux navigateurs |
| `web/static/*.css`, `index.html` | rechargement direct, sans compilation |
| `*.go` | le conteneur `app` recompile et relance le serveur ; la page se recharge à la reconnexion |

En cas d'erreur de typage ou de compilation, la dernière version valide reste servie et l'erreur s'affiche dans
les logs. Le mode dev (`DEV_MODE=1`) lit le frontend sur le disque au lieu du binaire ; il est **refusé au
démarrage si `APP_ENV` n'est pas `local`**, pour ne jamais tourner en prod par erreur.

Tests et vérification des types :

```sh
docker run --rm -v "$PWD:/src" -w /src golang:1.25-alpine go test ./...
docker compose -f compose.dev.yaml exec web npm run typecheck
```

Variables disponibles : voir [`.env.example`](.env.example).

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
| **Frontend TypeScript sans framework ni CDN, embarqué via `go:embed`** | Une seule dépendance npm (`typescript`), en dépendance de dev uniquement et en version exacte figée dans `package-lock.json` : elle sert à compiler, rien de tiers n'est envoyé au navigateur. Le mode strict détecte à la compilation les accès à des champs absents de l'API. Pas de script chargé depuis un autre domaine, rien à monter à côté du binaire : toute la page vient de la même origine. |
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
| **Contrôles de démo cloisonnés** | Les routes `/api/demo/*` modifient l'état du serveur : elles n'existent qu'avec la source mock, sans jeton seulement si `APP_ENV=local`, et ailleurs uniquement si `DEMO_TOKEN` (32 caractères min., comparaison à temps constant) est configuré. Avec les vraies sources, la page reste en lecture seule. |
| **Protection CSRF des commandes** | Les commandes exigent `Content-Type: application/json` : une page d'un autre site ne peut pas les envoyer sans requête CORS préliminaire, que le serveur n'accepte pas. Un en-tête `Origin` différent de l'hôte servi est refusé. Corps limité à 1 Kio, champs inconnus refusés, scénarios en liste blanche. |
| **Jeton de démo côté navigateur** | Saisi par le présentateur, jamais livré avec la page, conservé en `sessionStorage` (effacé à la fermeture de l'onglet) et retiré dès qu'il est refusé. |
