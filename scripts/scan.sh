#!/bin/sh
# Construit l'image puis la scanne en local avec Trivy (vulnérabilités) et
# Dockle (bonnes pratiques CIS Docker Benchmark), avec les mêmes seuils que la CI.
#
#   sh scripts/scan.sh            # échoue si une vulnérabilité HIGH/CRITICAL ou un WARN Dockle est trouvé
#   ENGINE=podman sh scripts/scan.sh
#
# Rapports : reports/trivy.sarif, reports/trivy.txt, reports/dockle.json (non commités).
#
# Sécurité : les scanners analysent une archive de l'image (docker save) et ne
# reçoivent PAS le socket Docker, qui donnerait un accès root à la machine.
set -eu

ENGINE=${ENGINE:-docker}
IMAGE=${IMAGE:-containermaxxing:scan}
# Outils figés par version + empreinte, comme les images de base.
TRIVY=docker.io/aquasec/trivy:0.76.2@sha256:8f6bb947dbc528902e11f2f5be52f7b519f6a39d5d148f41166f2040499768fb
DOCKLE=docker.io/goodwithtech/dockle:v0.4.15@sha256:eade932f793742de0aa8755406c7677cd7696f8675b6180926f7eeffa7abe6b9

cd "$(dirname "$0")/.."
mkdir -p reports
VERSION=$(git describe --tags --always 2>/dev/null || echo dev)

echo "==> build $IMAGE ($VERSION)"
"$ENGINE" build -t "$IMAGE" \
  --build-arg VERSION="$VERSION" \
  --build-arg COMMIT="$(git rev-parse HEAD 2>/dev/null || echo none)" \
  --build-arg CREATED="$(date -u +%Y-%m-%dT%H:%M:%SZ)" .

echo "==> export de l'image"
"$ENGINE" save -o reports/image.tar "$IMAGE"

# Les montages utilisent le chemin courant ; sous Git Bash (Windows), on évite
# la réécriture automatique des chemins Unix.
export MSYS_NO_PATHCONV=1
WORK="$(pwd)"
status=0

echo "==> Trivy : rapport complet (SARIF + texte)"
"$ENGINE" run --rm -v "$WORK/reports:/reports" -v trivy-cache:/root/.cache/trivy "$TRIVY" \
  image --input /reports/image.tar --format sarif --output /reports/trivy.sarif --quiet
"$ENGINE" run --rm -v "$WORK/reports:/reports" -v trivy-cache:/root/.cache/trivy "$TRIVY" \
  image --input /reports/image.tar --format table --output /reports/trivy.txt --quiet

echo "==> Trivy : contrôle bloquant (HIGH, CRITICAL)"
if ! "$ENGINE" run --rm -v "$WORK/reports:/reports" -v "$WORK/.trivyignore:/.trivyignore:ro" \
  -v trivy-cache:/root/.cache/trivy "$TRIVY" \
  image --input /reports/image.tar --severity HIGH,CRITICAL --exit-code 1 \
  --ignorefile /.trivyignore --quiet; then
  echo "!! Trivy : vulnérabilité HIGH/CRITICAL trouvée"
  status=1
fi

echo "==> Dockle (CIS Docker Benchmark, échec dès WARN)"
if ! "$ENGINE" run --rm -v "$WORK/reports:/reports" -v "$WORK/.dockleignore:/work/.dockleignore:ro" -w /work "$DOCKLE" \
  --input /reports/image.tar --exit-code 1 --exit-level warn \
  --format json --output /reports/dockle.json; then
  echo "!! Dockle : règle WARN ou FATAL non respectée"
  status=1
fi
"$ENGINE" run --rm -v "$WORK/reports:/reports" -v "$WORK/.dockleignore:/work/.dockleignore:ro" -w /work "$DOCKLE" \
  --input /reports/image.tar --exit-code 0 || true

rm -f reports/image.tar
if [ "$status" -eq 0 ]; then echo "==> OK : aucune vulnérabilité HIGH/CRITICAL, Dockle PASS"; fi
exit "$status"
