#!/bin/sh
# Boucle de dev (conteneur « app » de compose.dev.yaml) :
# recompile et relance le serveur dès qu'un fichier .go change.
# Scrutation toutes les secondes plutôt qu'inotify : les volumes montés depuis
# Windows/macOS ne remontent pas les événements fichiers au conteneur.
set -u

STAMP=/tmp/.dev-stamp
BIN=/tmp/server
PID=""

start() {
  touch "$STAMP" # avant le build : une modif pendant la compilation sera vue au tour suivant
  echo "[dev] compilation…"
  if go build -o "$BIN" ./cmd/server; then
    "$BIN" &
    PID=$!
  else
    echo "[dev] échec de compilation, en attente d'une correction"
    PID=""
  fi
}

stop() {
  if [ -n "$PID" ]; then
    kill "$PID" 2>/dev/null
    wait "$PID" 2>/dev/null
    PID=""
  fi
}

trap 'stop; exit 0' INT TERM

start
while true; do
  sleep 1
  if [ -n "$(find . -name '*.go' -newer "$STAMP" -not -path './node_modules/*' -print -quit)" ]; then
    echo "[dev] changement Go détecté, redémarrage"
    stop
    start
  fi
done
