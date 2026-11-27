# syntax=docker/dockerfile:1
#
# Template : application Go → binaire statique dans une image distroless non-root.
# Copier en « Dockerfile » à la racine du projet et adapter les lignes « À ADAPTER ».
#
#   docker build -t mon-app:dev \
#     --build-arg VERSION=$(git describe --tags --always) \
#     --build-arg COMMIT=$(git rev-parse HEAD) \
#     --build-arg CREATED=$(date -u +%Y-%m-%dT%H:%M:%SZ) .
#
# Prérequis :
#   - docker login dhi.io (Docker Hardened Images) ;
#   - le binaire répond à « healthcheck » en vérifiant lui-même sa santé (l'image
#     finale n'a ni shell ni curl) : voir template/README.md.
#
# Mettre à jour une image : docker buildx imagetools inspect <image:tag> donne
# l'empreinte actuelle à recopier après « @ ».

# --- Compilation ------------------------------------------------------------------
# Variante -dev : contient la chaîne de compilation, jamais l'image finale.
FROM dhi.io/golang:1.27-dev@sha256:121afb8b390b1d6babba8cd0a93eaf2ffa3735f835d388b9d79c2be38b41ee4f AS build
WORKDIR /src

# Dépendances d'abord : cette couche reste en cache tant que go.mod/go.sum ne changent pas.
COPY go.mod go.sum ./
RUN go mod download && go mod verify

# À ADAPTER : les dossiers de sources du projet.
COPY . .

ARG VERSION=dev
ARG COMMIT=none
# À ADAPTER : le paquet main (./cmd/app) et, si le code les déclare, les variables
# main.version / main.commit injectées au build.
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/app ./cmd/app

# --- Image finale -----------------------------------------------------------------
# Ni shell, ni gestionnaire de paquets, ni libc : seulement le binaire et la licence.
FROM gcr.io/distroless/static:nonroot@sha256:8ce6dd87e9823c5b48dcfb2c8604714ccb351ce59607327bcc4957448da279b5

# Le binaire appartient à root et n'est pas modifiable par le processus qui l'exécute.
COPY --from=build --chown=0:0 --chmod=0555 /out/app /app/app
COPY LICENSE /licenses/LICENSE

USER 65532:65532
ENV LISTEN_ADDR=:8080
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 CMD ["/app/app", "healthcheck"]

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
      org.opencontainers.image.base.name="gcr.io/distroless/static:nonroot"

ENTRYPOINT ["/app/app"]
