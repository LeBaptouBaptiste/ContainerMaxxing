FROM dhi.io/node:24-hardened-alpine@sha256:7734b5aa99b265412635bd9dcfad1ffeba6c13f08314567d51d4195b24f583bd AS web
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts
COPY tsconfig.json ./
COPY web/src/ ./web/src
RUN npm run build

FROM dhi.io/golang:1.27-hardened@sha256:31c6f959cf19705914d015661c058ebc155d9e068b2450e3b7f6e0e995b2b3d9 AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download && go mod verify
COPY cmd/ ./cmd
COPY internal/ ./internal
COPY web/ ./web
COPY --from=web /app/web/static/js ./web/static/js
RUN go build

FROM gcr.io/distroless/static@sha256:8ce6dd87e9823c5b48dcfb2c8604714ccb351ce59607327bcc4957448da279b5
WORKDIR /server
COPY --from=build /cmd/server /app/server
COPY --from=web /app/web/static/ /app/web/static/
USER 65532:65532
EXPOSE 8080
ENTRYPOINT [ "/app/server" ]