FROM dhi.io/node:24-alpine3.22-dev@sha256:f7c3a76cffba75be1ec64f1f8bfa435a2f265a8c48da9b7cc65212e328230226 AS web
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts
COPY tsconfig.json ./
COPY web/src/ ./web/src
RUN npm run build

FROM dhi.io/golang:1.27-dev@sha256:121afb8b390b1d6babba8cd0a93eaf2ffa3735f835d388b9d79c2be38b41ee4f AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd/ ./cmd
COPY internal/ ./internal
COPY web/ ./web
COPY --from=web /app/web/static/js ./web/static/js
ARG VERSION=dev
ARG COMMIT=none
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" -o /out/server ./cmd/server

FROM gcr.io/distroless/static:nonroot@sha256:8ce6dd87e9823c5b48dcfb2c8604714ccb351ce59607327bcc4957448da279b5
COPY --from=build --chown=0:0 --chmod=0555 /out/server /app/server
COPY LICENSE /licenses/LICENSE
USER 65532:65532
HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 CMD ["/app/server", "healthcheck"]
ENV LISTEN_ADDR=:8080
EXPOSE 8080

ARG VERSION=dev
ARG COMMIT=none
ARG CREATED=unknown
LABEL org.opencontainers.image.title="ContainerMaxxing" \
      org.opencontainers.image.description="Visualisation en direct de la chaîne de déploiement DevSecOps" \
      org.opencontainers.image.source="https://github.com/LeBaptouBaptiste/ContainerMaxxing" \
      org.opencontainers.image.url="https://github.com/LeBaptouBaptiste/ContainerMaxxing" \
      org.opencontainers.image.licenses="GPL-3.0-only" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${CREATED}" \
      org.opencontainers.image.base.name="gcr.io/distroless/static:nonroot"

ENTRYPOINT [ "/app/server" ]