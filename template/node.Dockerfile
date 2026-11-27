# syntax=docker/dockerfile:1
#
# Template : application Node.js → runtime Node durci (DHI), sans shell, non-root.
# Copier en « Dockerfile » à la racine du projet et adapter les lignes « À ADAPTER ».
#
#   docker build -t mon-app:dev \
#     --build-arg VERSION=$(git describe --tags --always) \
#     --build-arg COMMIT=$(git rev-parse HEAD) \
#     --build-arg CREATED=$(date -u +%Y-%m-%dT%H:%M:%SZ) .
#
# Prérequis :
#   - docker login dhi.io (Docker Hardened Images) ;
#   - package-lock.json versionné (npm ci) ;
#   - l'app expose GET /healthz (utilisé par la sonde de santé).
#
# Mettre à jour une image : docker buildx imagetools inspect <image:tag> donne
# l'empreinte actuelle à recopier après « @ ».

# --- Dépendances de production ----------------------------------------------------
# Variante -dev : contient npm, jamais l'image finale.
FROM dhi.io/node:24-alpine3.22-dev@sha256:f7c3a76cffba75be1ec64f1f8bfa435a2f265a8c48da9b7cc65212e328230226 AS deps
WORKDIR /app
COPY package.json package-lock.json ./
# Installation reproductible depuis le lockfile, sans exécuter les scripts des paquets.
# (mkdir : node_modules existe même sans dépendance, pour la copie de l'image finale)
RUN npm ci --omit=dev --ignore-scripts && mkdir -p node_modules

# --- Build (TypeScript, bundler…) -------------------------------------------------
# À ADAPTER : supprimer cette étape et la ligne « COPY --from=build » si le projet
# n'a pas d'étape de build (JavaScript servi tel quel).
FROM dhi.io/node:24-alpine3.22-dev@sha256:f7c3a76cffba75be1ec64f1f8bfa435a2f265a8c48da9b7cc65212e328230226 AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts
COPY . .
RUN npm run build

# --- Image finale -----------------------------------------------------------------
# Runtime Node seul : ni shell, ni npm, utilisateur non-root (1000).
FROM dhi.io/node:24-alpine3.22@sha256:dcd256005de53c181df697bbf9b78b282c538eaa4007c6399b9d241fa1c34623
WORKDIR /app

# Code et dépendances appartiennent à root, en lecture seule pour le processus.
COPY --from=deps  --chown=0:0 --chmod=0555 /app/node_modules ./node_modules
# À ADAPTER : le dossier produit par « npm run build » (dist/, build/…).
COPY --from=build --chown=0:0 --chmod=0555 /app/dist ./dist
COPY --chown=0:0 --chmod=0444 package.json ./
COPY LICENSE /licenses/LICENSE

USER 1000:1000
ENV NODE_ENV=production PORT=8080
EXPOSE 8080
# Pas de curl dans l'image : la sonde utilise fetch, intégré à Node.
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
  CMD ["node", "-e", "fetch('http://127.0.0.1:'+(process.env.PORT||8080)+'/healthz').then(r=>process.exit(r.ok?0:1),()=>process.exit(1))"]

ARG VERSION=dev
ARG COMMIT=none
ARG CREATED=unknown
# À ADAPTER : titre, description, dépôt et licence.
LABEL org.opencontainers.image.title="mon-app" \
      org.opencontainers.image.description="Description de l'application" \
      org.opencontainers.image.source="https://github.com/mon-org/mon-app" \
      org.opencontainers.image.licenses="GPL-3.0-only" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${CREATED}" \
      org.opencontainers.image.base.name="dhi.io/node:24-alpine3.22"

# À ADAPTER : le point d'entrée produit par le build.
ENTRYPOINT ["node", "dist/server.js"]
