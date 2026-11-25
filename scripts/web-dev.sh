#!/bin/sh
# Boucle de dev (conteneur « web » de compose.dev.yaml) :
# recompile le TypeScript dès qu'un fichier de web/src change.
# On n'utilise pas `tsc --watch` : il s'appuie sur les événements fichiers, que
# les volumes montés depuis Windows/macOS ne remontent pas au conteneur.
set -u

STAMP=/tmp/.web-stamp

build() {
  touch "$STAMP"
  if npx tsc --pretty; then
    echo "[web] $(date +%T) compilé"
  else
    echo "[web] $(date +%T) erreurs de typage : sortie inchangée (noEmitOnError)"
  fi
}

npm ci --no-fund --no-audit --loglevel=error
build
while true; do
  sleep 1
  if [ -n "$(find web/src tsconfig.json -newer "$STAMP" -print -quit)" ]; then
    build
  fi
done
