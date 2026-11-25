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
| 2 | Dockerfile distroless + scans locaux (Trivy, Dockle) | ✅ 0 vulnérabilité, Dockle PASS |
| 3 | compose : app, Postgres, Redis, durcissement (reverse proxy : NPM existant sur beacon) | 🟡 PostgreSQL et cache Redis côté app faits ; compose de prod à faire |
| 4 | Déploiement statique sur phobos (podman-compose, exposé par NPM sur beacon) | ⏳ |
| 5 | Branchement GitHub API | ⏳ |
| 6 | Workflow CI (build → Trivy → Dockle → GHCR (ghcr.io) → cosign → CaaS) | ⏳ |
| 7 | Déploiement Azure + branchement GHCR / Container Apps | ⏳ |
| 8 | Finitions et mode démo | 🟡 caméra, scénarios et mode présentateur faits |

## Architecture

```
cmd/server/          point d'entrée (serveur + sous-commande `healthcheck`)
internal/config/     configuration 100 % variables d'environnement
internal/model/      types partagés + validation des entrées
internal/source/     collecte de l'état (Mock aujourd'hui ; GitHub/GHCR/CaaS ensuite)
internal/live/       polling périodique + diffusion SSE (hub)
internal/store/      historique des déploiements : PostgreSQL (migrations/*.sql embarquées) ou mémoire
internal/cache/      cache à durée de vie courte : Redis (partagé) ou mémoire, tolérant aux pannes
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
avance sur une piste de 7 nœuds ; chaque nœud a un anneau qui se remplit au fil des steps de l'étape.

Une **caméra** suit la release : elle zoome sur l'étape en cours (le nœud en grand à gauche) et fait glisser à
droite un panneau de détails (steps avec chronomètre, infos clés, scans Trivy/Dockle, journal façon terminal).
Quand l'étape est terminée, la caméra **dézoome** sur la vue d'ensemble, le paquet avance, puis elle **rezoome**
sur l'étape suivante. Techniquement, c'est un simple `transform: translate() scale()` CSS sur la scène, calculé
en TypeScript à partir de la position du nœud (`chain.ts`).

Le cadrage change tout seul, selon trois modes :

| Mode | Quand | Comportement |
|------|-------|--------------|
| suivi en direct | une release est en cours ou vient d'échouer | la caméra suit l'étape active (`run.stage` du snapshot) |
| visite automatique | rien ne tourne | vue d'ensemble, puis zoom sur chaque étape tour à tour (7 s par plan) |
| sélection manuelle | clic sur un nœud, ◀ ▶ ou flèches du clavier | la caméra reste sur cette étape, puis le suivi auto reprend après 20 s |

Tant que le zoom est actif, la page passe en **mode scène** : la chaîne occupe toute la fenêtre (seul le bandeau
de version reste en haut). Le bouton « Vue d'ensemble » (touche `O`) coupe le zoom et revient à la page classique,
avec l'historique sous la chaîne ; « Mode scène » y revient. Le bouton « Plein écran » (touche `F`) passe en plus
le navigateur en plein écran (l'API Fullscreen exige un clic ou une touche, elle ne peut pas s'activer seule).

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
| `F` | plein écran du navigateur |
| `O` | vue d'ensemble : page classique avec l'historique / retour au mode scène |
| `←` `→` | change l'étape mise en avant |

Une release terminée n'est jamais relancée par `Espace` : un appui de trop ne doit pas effacer l'écran d'échec
pendant qu'on l'explique. On lance la suivante volontairement avec `1`–`4`.

Le bouton « Suivi auto » met le défilement en pause. Les animations sont désactivées si le système demande
`prefers-reduced-motion`.

Le serveur **interroge les sources lui-même** (polling toutes les 5 s par défaut) et pousse le dernier état aux
navigateurs via Server-Sent Events. Le navigateur ne parle jamais à GitHub ni à Azure.

### Base de données

L'historique est stocké dans PostgreSQL 16 (conteneur `postgres:16-alpine` sur phobos, Azure Database for
PostgreSQL Flexible Server sur Azure), via `DATABASE_URL`.

- **Schéma** : `deployments` et `deployment_steps` (`internal/store/migrations/001_init.sql`). Par rapport au sujet,
  `deployments` a en plus une colonne `status` (succès/échec, pour le taux d'échec) et `deployment_steps` une
  colonne `position` (ordre des steps).
- **Migrations au démarrage** : les fichiers `migrations/*.sql` sont embarqués dans le binaire et appliqués dans
  l'ordre, ceux déjà passés étant notés dans `schema_migrations`. Tout se fait dans une transaction protégée par un
  verrou consultatif (`pg_advisory_xact_lock`) : si plusieurs réplicas Container Apps démarrent ensemble, un seul
  migre ; en cas d'erreur, rien n'est appliqué.
- **Sans `DATABASE_URL`** : historique en mémoire, refusé hors `APP_ENV=local`.
- **Redémarrage** : en mode mock, l'état (version en prod, dernier tag…) est reconstitué depuis la base, donc
  l'historique continue après un `podman-compose down/up` au lieu de rejouer les mêmes versions.

Démo de la persistance (le volume nommé survit à `down`, tant qu'on ne passe pas `-v`) :

```sh
podman-compose down && podman-compose up -d   # l'historique et les stats sont toujours là
```

Tests d'intégration sur une vraie base (schéma temporaire, supprimé à la fin) :

```sh
TEST_DATABASE_URL='postgres://user:pass@host:5432/db?sslmode=disable' go test ./internal/store/
```

### Cache Redis

Le cache (`internal/cache`) se place devant les sources lentes ou soumises à quota. Il sert aujourd'hui à
l'historique (liste, détail, stats lus depuis PostgreSQL) et servira aux API GitHub et Azure quand les vraies
sources remplaceront le mock.

- **Redis partagé** (`REDIS_URL`) : toutes les instances (conteneurs, réplicas Container Apps) partagent le même
  cache, donc une API amont n'est appelée qu'une fois par durée de vie, quel que soit le nombre d'instances ou de
  visiteurs. Sans `REDIS_URL`, cache en mémoire du processus.
- **Une seule requête pour des appels simultanés** : plusieurs requêtes sur une clé absente ne déclenchent qu'un
  seul appel à la source (`singleflight`), ce qui évite l'effet « ruée » à l'expiration.
- **Invalidation par génération** : les clés de liste et de stats contiennent un numéro de génération, incrémenté
  dans Redis à chaque nouveau déploiement. Le nouveau déploiement est visible immédiatement, sur toutes les instances.
- **Tolérant aux pannes** : si Redis est arrêté ou lent (500 ms max par commande), l'app interroge directement la
  source et continue de fonctionner ; l'incident est journalisé au plus une fois toutes les 30 s.

Configuration recommandée côté infra (c'est un pur cache) : pas de persistance (`--save "" --appendonly no`),
mémoire bornée avec éviction (`--maxmemory 64mb --maxmemory-policy allkeys-lru`), mot de passe
(`--requirepass` via un secret), aucun port publié, réseau interne uniquement.

### Exposition sur phobos (Nginx Proxy Manager sur beacon)

Le sujet prévoyait un Caddy dans le compose. Nous utilisons à la place le **Nginx Proxy Manager déjà en place sur
beacon**, qui gère déjà le TLS (Let's Encrypt) de nos sous-domaines : un second reverse proxy n'aurait rien apporté
de plus, et un service de moins, c'est une surface d'attaque en moins.

```
Internet ─HTTPS─▶ NPM (beacon) ─HTTP─▶ app (phobos, seul port publié) ─▶ postgres, redis (réseau interne)
                  containers2v-statique.voyager3.fr
```

- **L'app est le seul service qui publie un port** ; Postgres et Redis restent sur un réseau compose `internal: true`.
- **Ce port n'est joignable que depuis beacon** : publié sur l'interface privée de phobos (pas `0.0.0.0`) et filtré
  par le pare-feu sur l'IP de beacon.
- **Le TLS s'arrête sur beacon** : le tronçon beacon → phobos est en HTTP clair, ce qui suppose un réseau privé (LAN ou
  VPN) entre les deux machines. C'est un point à assumer et à mentionner en soutenance.
- **Les en-têtes de sécurité viennent de l'app elle-même** (CSP, `nosniff`, `frame-ancestors`…), donc ils ne
  dépendent pas du proxy. Côté NPM : activer « Force SSL », HSTS et HTTP/2.
- **Le flux SSE passe à travers NPM sans réglage** : l'app envoie `X-Accel-Buffering: no` (nginx ne met pas le flux en
  tampon) et un ping toutes les 20 s, sous le délai d'inactivité par défaut de nginx (60 s).

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

## Image de conteneur

Le `Dockerfile` construit l'image en trois étapes : TypeScript → JavaScript (`node`), binaire Go statique (`golang`),
puis une image finale `gcr.io/distroless/static:nonroot` qui ne contient **que le binaire et la licence** (≈ 5,5 Mo
compressée).

```sh
docker build -t containermaxxing:dev \
  --build-arg VERSION=$(git describe --tags --always) \
  --build-arg COMMIT=$(git rev-parse HEAD) \
  --build-arg CREATED=$(date -u +%Y-%m-%dT%H:%M:%SZ) .

# lancement durci, comme dans le compose
docker run --rm -p 127.0.0.1:8080:8080 --read-only --cap-drop ALL \
  --security-opt no-new-privileges:true containermaxxing:dev
```

### Scans locaux (mêmes seuils que la CI)

```sh
sh scripts/scan.sh            # ou ENGINE=podman sh scripts/scan.sh
```

Le script construit l'image, puis lance :

- **Trivy** : rapport complet (`reports/trivy.sarif`, `reports/trivy.txt`) et contrôle **bloquant sur HIGH/CRITICAL** ;
- **Dockle** (CIS Docker Benchmark) : **bloquant dès le niveau WARN** (`reports/dockle.json`).

Les exceptions sont versionnées et justifiées : `.trivyignore` (vide) et `.dockleignore` (seulement `CIS-DI-0005`,
Docker Content Trust, remplacé par la signature cosign). Résultat actuel : **0 vulnérabilité, Dockle PASS**.

> **Le scan a déjà servi** : la première image, compilée en Go 1.25.14, a été bloquée par Trivy (4 HIGH dans la
> bibliothèque standard : `crypto/x509`, `net/http`, `crypto/tls`). Ces failles n'étaient corrigées que dans les
> branches 1.26/1.27 : Go 1.25 n'est plus maintenu. Correction appliquée : passage à **Go 1.27.4** (et mise à jour des
> dépendances), plutôt qu'une exception dans `.trivyignore`.

## Déploiement sur phobos (podman-compose)

### Secrets

Les secrets du compose sont des **fichiers** dans `secrets/` (ignoré par git), générés une fois sur le serveur :

```sh
sh scripts/init-secrets.sh
```

| Fichier | Contenu |
|---------|---------|
| `postgres_password` | mot de passe du rôle PostgreSQL |
| `database_url` | DSN de l'app (contient ce mot de passe) |
| `redis_acl` | liste de contrôle d'accès Redis : l'app a un mot de passe et aucune commande dangereuse (`FLUSHALL`, `CONFIG`, `KEYS`…) ; l'utilisateur `healthcheck` ne peut faire que `PING` |
| `redis_url` | URL Redis de l'app (contient le mot de passe) |
| `ingest_token` | jeton Bearer de `POST /api/deployments` |

- **Valeurs aléatoires** de 256 bits (`/dev/urandom`), en hexadécimal pour pouvoir figurer dans une URL sans échappement.
- **Jamais écrasés** : relancer le script conserve les secrets existants (sinon la base existante deviendrait
  inaccessible).
- **Droits** : dossier en `700` (fermé aux autres utilisateurs du serveur), fichiers lisibles par les utilisateurs
  non-root des conteneurs.
- **Montés comme fichiers** dans `/run/secrets` (variantes `*_FILE`) et non passés en variables d'environnement :
  ils n'apparaissent ni dans `podman inspect` ni dans l'environnement des processus.

### Lancer la pile

```sh
sh scripts/init-secrets.sh          # une seule fois
podman-compose up -d --build        # construit l'image et démarre app + postgres + redis
podman-compose ps
```

Configuration non secrète surchargeable dans un `.env` (non commité) à côté de `compose.yaml` :

| Variable | Défaut | Rôle |
|----------|--------|------|
| `APP_BIND` | `127.0.0.1` | interface où l'app publie son port : l'**IP privée de phobos**, joignable par beacon (jamais `0.0.0.0`) |
| `APP_PORT` | `8080` | port publié, cible du proxy host NPM |
| `APP_ENV` | `phobos` | environnement affiché dans le bandeau |
| `DATA_SOURCE` | `mock` | source des données |
| `APP_IMAGE` | `localhost/containermaxxing:latest` | nom de l'image construite (ou image GHCR) |

- **App** : construite depuis le `Dockerfile`, seul service qui publie un port.
- **PostgreSQL 16** : volume nommé `pgdata` (survit à `podman-compose down`, pas à `down -v`), authentification
  `scram-sha-256`, mot de passe lu depuis le secret.
- **Redis 7** : pur cache (pas de persistance, 64 Mo, éviction LRU), accès contrôlé par le fichier ACL.

Démo de persistance : `podman-compose down && podman-compose up -d` → l'historique est intact.

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
docker run --rm -v "$PWD:/src" -w /src golang:1.27-alpine go test ./...
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
| **Sous-commande `healthcheck`** | L'image distroless n'a ni `curl` ni shell : le binaire vérifie lui-même `/healthz` pour le `HEALTHCHECK`. |

### Étape 2 — image de conteneur

| Choix | Pourquoi |
|-------|----------|
| **Build multi-stage** | Les outils de build (Node, compilateur Go, `npm`, sources) restent dans les étapes intermédiaires : l'image finale ne contient que le binaire. Moins de composants = moins de vulnérabilités possibles. |
| **`distroless/static:nonroot`** | Ni shell, ni gestionnaire de paquets, ni libc : un attaquant qui exécuterait du code dans le conteneur n'a aucun outil à disposition (`sh` introuvable, vérifié). Trivy : 0 vulnérabilité sur la couche système. |
| **Utilisateur non-root (65532)** | Le processus ne tourne pas en root ; le binaire appartient à root en lecture/exécution seule (`0555`), il ne peut pas être modifié par le processus qui l'exécute. |
| **Binaire statique `CGO_ENABLED=0`, `-trimpath`, `-s -w`** | Aucune dépendance à une libc ; aucun chemin de la machine de build ni symbole de debug dans le binaire. |
| **Images figées par empreinte (`@sha256:…`)** | Un tag (`:24-alpine`, `:nonroot`) peut être redirigé vers une autre image sur le registre ; une empreinte désigne un contenu exact. Idem pour les images de Trivy et Dockle dans `scripts/scan.sh`. |
| **`npm ci --ignore-scripts` et `go mod verify`** | Installation reproductible depuis les fichiers de verrouillage, sans exécuter les scripts d'installation des paquets npm (vecteur classique d'attaque de la chaîne d'approvisionnement) ; modules Go vérifiés contre `go.sum`. |
| **`.dockerignore` restrictif** | `.env`, `secrets/`, clés, `.git` ne sont jamais envoyés au contexte de build : impossible de les copier par erreur dans une couche de l'image. |
| **Compatible lecture seule** | L'app n'écrit rien sur disque : le conteneur tourne avec `--read-only`, `--cap-drop ALL` et `no-new-privileges` (vérifié). |
| **Labels OCI** | Source, licence (GPL-3.0), version, commit et date de build lisibles avec `docker inspect` : on sait toujours d'où vient une image. |
| **Scans locaux avant la CI, sans socket Docker** | Trivy et Dockle analysent une archive de l'image (`docker save`) : ils ne reçoivent pas le socket Docker, qui donnerait un accès root à la machine. Les mêmes seuils qu'en CI permettent de corriger avant de pousser. |
| **Requêtes SQL paramétrées** | Aucune valeur n'est concaténée dans le SQL (`$1, $2…`) : pas d'injection SQL possible depuis l'API d'ingestion. |
| **Contraintes `CHECK` en base** | La base revalide les domaines (statuts, cible, digest `sha256:…`, `run_url` en `https://github.com/…`, compteurs ≥ 0) : même une écriture qui contournerait l'application ne peut pas stocker de valeur dangereuse. Défense en profondeur avec la validation de l'API. |
| **`DATABASE_URL` traité comme un secret** | Il contient le mot de passe : lu de préférence depuis un fichier (`DATABASE_URL_FILE`), jamais journalisé, et les erreurs de parsing ne le recopient pas. |
| **Chiffrement vers la base** | Sur Azure, connexion en `sslmode=verify-full` (certificat du serveur vérifié avec les autorités de l'image distroless). Sur phobos, la base est sur un réseau compose interne sans port publié. |
| **Pool borné et timeouts** | 5 connexions max, 5 s max par requête, 5 s pour se connecter : une base lente ne bloque pas l'app ni n'épuise ses ressources. |
| **Cache non critique (fail-open)** | Redis est une optimisation, pas une dépendance : s'il tombe, l'app sert les données depuis la source. Un incident sur le cache ne devient pas une panne de l'application. |
| **Cache borné** | Valeurs de plus de 1 Mio jamais mises en cache, cache mémoire limité à 1 000 entrées, Redis configuré avec `maxmemory` + éviction LRU : le cache ne peut pas épuiser la mémoire. |
| **Erreurs jamais mises en cache** | Une réponse en erreur (API amont indisponible) n'est pas conservée : elle ne peut pas « empoisonner » le cache pendant toute sa durée de vie. |
| **`REDIS_URL` traité comme un secret** | Il peut contenir le mot de passe : variante `REDIS_URL_FILE`, jamais journalisé. Sur Azure Cache for Redis, utiliser `rediss://` (TLS). |
| **Contrôles de démo cloisonnés** | Les routes `/api/demo/*` modifient l'état du serveur : elles n'existent qu'avec la source mock, sans jeton seulement si `APP_ENV=local`, et ailleurs uniquement si `DEMO_TOKEN` (32 caractères min., comparaison à temps constant) est configuré. Avec les vraies sources, la page reste en lecture seule. |
| **Protection CSRF des commandes** | Les commandes exigent `Content-Type: application/json` : une page d'un autre site ne peut pas les envoyer sans requête CORS préliminaire, que le serveur n'accepte pas. Un en-tête `Origin` différent de l'hôte servi est refusé. Corps limité à 1 Kio, champs inconnus refusés, scénarios en liste blanche. |
| **Jeton de démo côté navigateur** | Saisi par le présentateur, jamais livré avec la page, conservé en `sessionStorage` (effacé à la fermeture de l'onglet) et retiré dès qu'il est refusé. |

### Étape 3 — compose de prod

| Choix | Pourquoi |
|-------|----------|
| **Aucun conteneur en root** | L'app tourne en `65532`, PostgreSQL en `70`, Redis en `999` dès le démarrage : les entrypoints n'ont pas besoin de passer par root puis de changer d'utilisateur. Un processus compromis n'a pas les droits root dans le conteneur. |
| **`cap_drop: [ALL]`** | Aucune capacité Linux (changer de propriétaire, ouvrir un port < 1024, contourner les droits de fichiers…) : c'est possible parce qu'aucun service ne démarre en root. |
| **`no-new-privileges`** | Un binaire setuid ne peut pas élever ses privilèges, même s'il en existait un dans l'image. |
| **`read_only: true`** | Système de fichiers en lecture seule pour les trois services : un attaquant ne peut ni déposer un outil ni modifier un binaire. Seuls restent inscriptibles le volume `pgdata` et des `tmpfs` en mémoire (socket et fichiers temporaires de PostgreSQL). |
| **Limites de ressources** | Mémoire (`mem_limit`) et nombre de processus (`pids_limit`) bornés par service : une fuite mémoire ou une « fork bomb » reste confinée à son conteneur. |
| **Secrets en fichiers** | Montés dans `/run/secrets` et lus via `*_FILE` : jamais dans l'environnement ni dans `podman inspect`. |
| **Redis protégé par ACL** | Mot de passe obligatoire, commandes dangereuses interdites à l'app (`FLUSHALL`, `CONFIG`, `KEYS`…), utilisateur de sonde limité à `PING`. |
| **Port publié sur l'IP privée** | `APP_BIND` vaut `127.0.0.1` par défaut et doit être réglé sur l'IP privée de phobos : jamais `0.0.0.0`. |
| **Réseaux séparés** | `backend` est `internal: true` : PostgreSQL et Redis n'ont aucun accès à Internet (impossible d'exfiltrer des données ou de télécharger un outil) et ne peuvent pas publier de port. Seule l'app est aussi sur `edge`, le réseau qui porte son port publié (et, plus tard, ses appels à GitHub/Azure). |
