#!/bin/sh
# Génère les secrets du compose dans secrets/ (non commité, voir .gitignore).
# N'écrase jamais un secret existant : relancer le script est sans danger.
#
#   sh scripts/init-secrets.sh
#
# Dossier en 700 (fermé aux autres utilisateurs de l'hôte) ; fichiers en 644 pour
# que les utilisateurs non-root des conteneurs puissent lire leur secret monté.
# Fichiers créés :
#   postgres_password   mot de passe du rôle PostgreSQL
#   database_url        DSN complet de l'app (contient le mot de passe ci-dessus)
#   redis_acl           liste de contrôle d'accès Redis (utilisateurs, droits)
#   redis_url           URL Redis de l'app (contient le mot de passe)
#   ingest_token        jeton Bearer de POST /api/deployments (CI)
set -eu

cd "$(dirname "$0")/.."
umask 077
mkdir -p secrets
chmod 700 secrets

# 32 octets aléatoires en hexadécimal (64 caractères) : sans caractère spécial,
# donc utilisable tel quel dans une URL.
rand() { od -An -N32 -tx1 /dev/urandom | tr -d ' \n'; }

write() { # write <fichier> <contenu> : seulement si le fichier n'existe pas encore
  if [ -e "secrets/$1" ]; then
    echo "  = secrets/$1 (existant, conservé)"
  else
    printf '%s' "$2" > "secrets/$1"
    echo "  + secrets/$1"
  fi
}

echo "==> secrets/"
write postgres_password "$(rand)"
write database_url "postgres://containermaxxing:$(cat secrets/postgres_password)@postgres:5432/containermaxxing?sslmode=disable"

[ -e secrets/redis_password ] || rand > secrets/redis_password
REDIS_PASSWORD=$(cat secrets/redis_password)
# - default : utilisateur de l'app, mot de passe obligatoire, toutes les clés,
#   mais aucune commande « dangereuse » (FLUSHALL, CONFIG, KEYS, DEBUG, ACL…).
# - healthcheck : sans mot de passe, ne peut exécuter que PING.
write redis_acl "user default on >$REDIS_PASSWORD ~* &* +@all -@dangerous
user healthcheck on nopass -@all +ping
"
write redis_url "redis://:$REDIS_PASSWORD@redis:6379/0"
write ingest_token "$(rand)"

chmod 644 secrets/*
echo "==> OK. Ne jamais committer ni copier ce dossier hors du serveur."
