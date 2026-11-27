# Template : conteneurs durcis + CI/CD sécurisée

Kit pour donner à n'importe quel projet le système de conteneurs et la chaîne CI/CD de ContainerMaxxing :
une image minimale non-root, une pile compose durcie et une CI qui scanne, signe et déploie.

```
Dockerfile (DHI → distroless)   compose.yaml (app + PostgreSQL + Redis)   CI réutilisable
        │                               │                                      │
   scan.sh en local ───────────── podman-compose sur un serveur      build → Trivy → Dockle → SBOM
   (mêmes seuils que la CI)                                          → GHCR + cosign → Azure Container Apps
```

## Contenu

| Fichier | Rôle | À copier vers |
|---------|------|---------------|
| `go.Dockerfile` | Go → binaire statique dans `distroless/static:nonroot` | `Dockerfile` |
| `node.Dockerfile` | Node.js → runtime Node durci DHI, sans shell | `Dockerfile` |
| `compose.yaml` | app + PostgreSQL + Redis durcis (base et cache supprimables) | `compose.yaml` |
| `app.env.example` | variables du compose, sans secret | `app.env` |
| `scripts/init-secrets.sh` | génère les secrets du compose dans `secrets/` | `scripts/` |
| `scripts/scan.sh` | build + Trivy + Dockle en local | `scripts/` |
| `.dockerignore`, `.trivyignore`, `.dockleignore` | contexte de build minimal, exceptions de scan justifiées | racine |
| `.github/workflows/ci.yml` | appel de la CI réutilisable | `.github/workflows/ci.yml` |

La CI elle-même n'est pas copiée : elle vit dans ce dépôt (`.github/workflows/container-ci.yml`) et votre `ci.yml`
l'appelle. Les correctifs de sécurité de la chaîne profitent ainsi à tous les projets qui l'utilisent.

## Mise en place

### 1. Prérequis

- **Docker Hardened Images** : un compte Docker avec accès à `dhi.io` (images de build et images Postgres/Redis).
  En local : `docker login dhi.io`.
- **Application** :
  - elle écoute sur le port **8080** (ou adapter `EXPOSE`, `ports` et `target-port`) ;
  - elle expose une **sonde de santé** : sous-commande `healthcheck` du binaire pour Go (l'image n'a ni shell ni
    curl, voir l'exemple ci-dessous), route `GET /healthz` pour Node ;
  - elle lit ses secrets depuis des **fichiers** : `DATABASE_URL_FILE`, `REDIS_URL_FILE`… contiennent le chemin du
    fichier, jamais la valeur ;
  - elle n'écrit rien sur son système de fichiers (le conteneur est en lecture seule) ; sinon, ajouter un `tmpfs`.
- Un fichier `LICENSE` à la racine (copié dans l'image).

Sonde de santé en Go, sans dépendance :

```go
if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
	r, err := http.Get("http://127.0.0.1" + os.Getenv("LISTEN_ADDR") + "/healthz")
	if err != nil || r.StatusCode != http.StatusOK {
		os.Exit(1)
	}
	return
}
```

### 2. Copier et adapter les fichiers

```sh
T=chemin/vers/ContainerMaxxing/template
cp $T/go.Dockerfile Dockerfile            # ou node.Dockerfile
cp $T/compose.yaml $T/.dockerignore $T/.trivyignore $T/.dockleignore .
cp $T/app.env.example app.env
mkdir -p scripts .github/workflows && cp $T/scripts/*.sh scripts/ && cp $T/.github/workflows/ci.yml .github/workflows/
printf 'secrets/\nreports/\n*.sarif\n' >> .gitignore
```

Puis chercher **« À ADAPTER »** dans chaque fichier : chemin du paquet `main` ou dossier de build, labels OCI, nom
du projet compose, variables et utilisateur de l'app, image GHCR de la CI.

### 3. Vérifier en local

```sh
sh scripts/scan.sh                                  # build + Trivy + Dockle : doit finir par « OK »
sh scripts/init-secrets.sh                          # une seule fois
docker compose --env-file app.env up -d --build     # ou podman-compose
docker compose --env-file app.env ps                # tous les services « healthy »
```

### 4. Brancher la CI

Dans *Settings → Secrets and variables → Actions* du dépôt :

| Secret | Où | Utilisé pour |
|--------|----|--------------|
| `DHI_USERNAME`, `DHI_TOKEN` | **Repository secrets** | connexion à `dhi.io` au build (jeton Docker en lecture seule) |
| `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_SUBSCRIPTION_ID` | environnement `prod` | déploiement Azure (facultatif) |
| `INGEST_TOKEN` | environnement `prod` | rapport vers une app ContainerMaxxing (facultatif) |

Les secrets d'environnement ne sont lus que par les jobs de déploiement : une PR n'y a pas accès.

**Chaque secret utilisé doit être transmis par son nom dans `ci.yml`** (bloc `secrets:` de l'appel), y compris
ceux qui n'existent que dans l'environnement `prod` : un secret non transmis est vide dans la CI réutilisable, et
GitHub n'affiche aucune erreur (la connexion à Azure échoue alors avec « Ensure 'client-id' and 'tenant-id' are
supplied »). Décommenter les lignes Azure de l'exemple si le déploiement est activé.

Ensuite :

- **Pull request** : build, Trivy, Dockle, SBOM et audit de la CI. Rien n'est publié.
- **Push sur `main`** : image publiée sur GHCR (tags `main` et `sha-xxxxxxx`), signée et SBOM attesté.
- **Tag `vX.Y.Z`** : déploiement Azure Container Apps si `aca-app` est renseigné, puis release GitHub.

### 5. Déploiement Azure Container Apps (facultatif)

1. Créer la Container App et une identité Azure (App registration) avec le rôle *Contributor* sur son groupe de
   ressources.
2. Ajouter à cette identité un **credential fédéré** GitHub pour l'environnement `prod` du dépôt
   (sujet `repo:<org>/<dépôt>:environment:prod`) : la CI se connecte en OIDC, sans mot de passe.
3. Renseigner les trois secrets Azure dans l'environnement `prod`, puis `aca-app`, `aca-resource-group` et
   `app-url` dans `ci.yml`.

## Paramètres de la CI réutilisable

| Paramètre | Défaut | Rôle |
|-----------|--------|------|
| `image` | **obligatoire** | image GHCR sans tag, en minuscules (`ghcr.io/mon-org/mon-app`) |
| `context`, `dockerfile` | `.`, `Dockerfile` | contexte et Dockerfile du build |
| `dhi-login` | `false` | connexion à `dhi.io` avant le build |
| `audit-ci` | `true` | audit de la CI avec plumber (résultats dans *Security → Code scanning*) |
| `dependency-graph` | `true` | SBOM envoyé au graphe de dépendances (alertes Dependabot) |
| `release` | `true` | release GitHub avec SBOM et rapports de scan sur chaque tag |
| `environment` | `prod` | environnement GitHub du déploiement |
| `aca-app`, `aca-resource-group` | vide | Container App à mettre à jour (vide = pas de déploiement) |
| `target-port` | `8080` | port d'écoute du conteneur |
| `app-url` | vide | URL publique, pour le smoke test |
| `smoke-path` | `/healthz` | chemin qui doit répondre 2xx après le déploiement |
| `version-path`, `version-jq` | vide, `.version` | contrôle que la nouvelle version est bien servie (ex. `/api/status`, `.app_version`) |
| `report-url` | vide | endpoint qui reçoit le rapport de déploiement (app ContainerMaxxing) |

Sorties : `version` (tag ou `sha-xxxxxxx`) et `digest` de l'image publiée.

Pour vérifier une image publiée :

```sh
cosign verify ghcr.io/mon-org/mon-app@sha256:… \
  --certificate-identity "https://github.com/LeBaptouBaptiste/ContainerMaxxing/.github/workflows/container-ci.yml@refs/heads/main" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-github-workflow-repository mon-org/mon-app
```

L'identité est celle du workflow réutilisable, à la ref indiquée dans `uses:`. Chaque release GitHub donne la commande
exacte.

## Choix de sécurité

### Image

| Choix | Pourquoi |
|-------|----------|
| **Build multi-stage** | Compilateur, npm et sources restent dans les étapes intermédiaires : l'image finale ne contient que l'exécutable. |
| **Images de build durcies (DHI), image finale minimale** | Distroless (Go) ou runtime Node DHI : ni shell, ni gestionnaire de paquets. Un attaquant qui exécute du code n'a aucun outil à disposition. |
| **Non-root, code en lecture seule** | `USER` non-root ; binaire ou code en root:root `0555` : le processus ne peut pas modifier ce qu'il exécute. |
| **Images figées par empreinte** | Un tag peut être redirigé vers une autre image ; `@sha256:…` désigne un contenu exact. |
| **`npm ci --ignore-scripts`, `go mod verify`** | Installation reproductible depuis les fichiers de verrouillage, sans exécuter de script d'installation tiers. |
| **`.dockerignore` restrictif** | Secrets, clés, `.git` et `.env` ne peuvent pas finir dans une couche de l'image. |
| **Labels OCI** | Source, licence, version, commit et date lisibles avec `docker inspect`. |
| **Scans locaux sans socket Docker** | Trivy et Dockle analysent une archive (`docker save`) : ils n'obtiennent pas l'accès root que donne le socket. |

### Compose

| Choix | Pourquoi |
|-------|----------|
| **Aucun conteneur en root, `cap_drop: [ALL]`, `no-new-privileges`** | Un processus compromis n'a ni droits root, ni capacités Linux, ni moyen d'en obtenir. |
| **`read_only: true`** | Rien ne peut être déposé ou modifié dans les conteneurs ; seuls le volume de la base et des `tmpfs` sont inscriptibles. |
| **Réseau `backend` interne** | PostgreSQL et Redis n'ont ni accès à Internet ni port publié. |
| **Port publié sur une interface précise** | `APP_BIND` vaut `127.0.0.1` ou une IP privée, jamais `0.0.0.0`. |
| **Secrets en fichiers** | Montés dans `/run/secrets`, lus via `*_FILE` : absents de l'environnement et de `docker inspect`. |
| **Redis protégé par ACL, PostgreSQL en `scram-sha-256`** | Mot de passe obligatoire, commandes dangereuses interdites, utilisateur de sonde limité à `PING`. |
| **Limites de ressources** | `mem_limit` et `pids_limit` : une fuite ou une *fork bomb* reste confinée. |
| **Sondes et démarrage ordonné** | L'app attend PostgreSQL et Redis `healthy` ; une panne est visible au lieu d'un service à moitié démarré. |

### CI/CD

| Choix | Pourquoi |
|-------|----------|
| **Actions épinglées par SHA, scanners par empreinte** | Un tag d'action peut être déplacé ; un SHA désigne un code exact. |
| **Permissions minimales par job** | Chaque job ne reçoit que ce dont il a besoin ; le job appelant fixe le plafond. |
| **Aucun secret longue durée pour publier et déployer** | GHCR avec le jeton éphémère du run, Azure et Sigstore en OIDC. |
| **Politique bloquante** | Échec sur CVE HIGH/CRITICAL corrigeable, secret dans l'image ou contrôle Dockle FATAL. |
| **Image publiée = image scannée** | Pas de second build entre le scan et la publication. |
| **Signature keyless + attestation SBOM** | Preuve vérifiable de l'origine de l'image et de son contenu. |
| **Vérification avant déploiement, déploiement par digest** | Seule une image signée par cette CI, pour ce dépôt et cette ref, est déployée, et par son empreinte exacte. |
| **Graphe de dépendances** | Une CVE publiée après la release déclenche une alerte Dependabot. |
