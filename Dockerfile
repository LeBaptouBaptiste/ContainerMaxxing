# syntax=docker/dockerfile:1
#
# Image ContainerMaxxing : build multi-stage, image finale distroless sans shell.
#
#   docker build -t containermaxxing:dev \
#     --build-arg VERSION=$(git describe --tags --always) \
#     --build-arg COMMIT=$(git rev-parse HEAD) \
#     --build-arg CREATED=$(date -u +%Y-%m-%dT%H:%M:%SZ) .
#
# Les images de base sont figées par empreinte (digest) : un tag peut être
# déplacé sur le registre, une empreinte non. Le tag reste indiqué pour la
# lisibilité et pour les outils de mise à jour (Dependabot/Renovate).

# --- 1. Frontend : TypeScript → JavaScript ----------------------------------------
FROM docker.io/library/node:24-alpine@sha256:116b800c3754b440ee0aa3ed2f20f5bf3d99a554bfd9adeea09ea8017f6e85f5 AS web
WORKDIR /src
COPY package.json package-lock.json tsconfig.json ./
# --ignore-scripts : aucun script d'installation de paquet npm n'est exécuté.
RUN npm ci --ignore-scripts --no-audit --no-fund
COPY web/src ./web/src
RUN npm run build

# --- 2. Backend : binaire Go statique ----------------------------------------------
FROM docker.io/library/golang:1.27-alpine@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS build
WORKDIR /src
# Dépendances d'abord (couche mise en cache), vérifiées contre go.sum.
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web
COPY --from=web /src/web/static/js ./web/static/js
ARG VERSION=dev
ARG COMMIT=none
# CGO_ENABLED=0 : binaire 100 % statique, sans libc → compatible distroless/static.
# -trimpath : aucun chemin de la machine de build dans le binaire.
# -s -w : sans table des symboles ni infos de debug (binaire plus petit).
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false \
      -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/server ./cmd/server

# --- 3. Image finale : distroless, non-root, sans shell ----------------------------
FROM gcr.io/distroless/static:nonroot@sha256:8ce6dd87e9823c5b48dcfb2c8604714ccb351ce59607327bcc4957448da279b5

ARG VERSION=dev
ARG COMMIT=none
ARG CREATED=unknown
LABEL org.opencontainers.image.title="ContainerMaxxing" \
      org.opencontainers.image.description="Visualisation en direct de la chaîne de déploiement DevSecOps" \
      org.opencontainers.image.source="https://github.com/LeBaptouBaptiste/ContainerMaxxing" \
      org.opencontainers.image.url="https://github.com/LeBaptouBaptiste/ContainerMaxxing" \
      org.opencontainers.image.licenses="GPL-3.0-only" \
      org.opencontainers.image.vendor="ContainerMaxxing" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${CREATED}" \
      org.opencontainers.image.base.name="gcr.io/distroless/static:nonroot"

# Seuls le binaire (frontend embarqué) et la licence entrent dans l'image.
COPY --from=build --chown=0:0 --chmod=0555 /out/server /server
COPY --chown=0:0 --chmod=0444 LICENSE /licenses/LICENSE

# 65532 = utilisateur « nonroot » de distroless. Le binaire appartient à root et
# n'est pas modifiable par l'utilisateur qui l'exécute.
USER 65532:65532
ENV LISTEN_ADDR=:8080
EXPOSE 8080

# Pas de shell ni de curl dans l'image : le binaire vérifie lui-même /healthz.
# (Podman ignore HEALTHCHECK en format OCI : le compose redéfinit la sonde.)
HEALTHCHECK --interval=15s --timeout=3s --start-period=10s --retries=3 \
  CMD ["/server", "healthcheck"]

ENTRYPOINT ["/server"]
