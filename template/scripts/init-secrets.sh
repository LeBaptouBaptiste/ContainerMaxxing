#!/bin/sh
# Génère les secrets du compose dans secrets/ (non commité, voir .gitignore).
# N'écrase jamais un secret existant : relancer le script est sans danger.
#
#   sh scripts/init-secrets.sh
#   POSTGRES=0 sh scripts/init-secrets.sh     # projet sans PostgreSQL
#   REDIS=0 sh scripts/init-secrets.sh        # projet sans Redis
#
# Dossier en 700 (fermé aux autres utilisateurs de l'hôte) ; fichiers en 644 pour
# que les utilisateurs non-root des conteneurs puissent lire leur secret monté.
# Fichiers créés (noms attendus par template/compose.yaml) :
#   postgres_password   mot de passe du rôle PostgreSQL
#   database_url        DSN complet de l'app (contient le mot de passe ci-dessus)
#   redis_acl           liste de contrôle d'accès Redis (utilisateurs, droits)
#   redis_url           URL Redis de l'app (contient le mot de passe)
set -eu

POSTGRES=${POSTGRES:-1}
REDIS=${REDIS:-1}
# Doivent correspondre à POSTGRES_USER / POSTGRES_DB du compose.
DB_USER=${DB_USER:-app}
DB_NAME=${DB_NAME:-app}

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
if [ "$POSTGRES" = 1 ]; then
  write postgres_password "$(rand)"
  write database_url "postgres://$DB_USER:$(cat secrets/postgres_password)@postgres:5432/$DB_NAME?sslmode=disable"
fi

if [ "$REDIS" = 1 ]; then
  [ -e secrets/redis_password ] || rand > secrets/redis_password
  REDIS_PASSWORD=$(cat secrets/redis_password)
  # - default : utilisateur de l'app, mot de passe obligatoire, toutes les clés,
  #   mais aucune commande « dangereuse » (FLUSHALL, CONFIG, KEYS, DEBUG, ACL…).
  # - healthcheck : sans mot de passe, ne peut exécuter que PING.
  write redis_acl "user default on >$REDIS_PASSWORD ~* &* +@all -@dangerous
user healthcheck on nopass -@all +ping
"
  write redis_url "redis://:$REDIS_PASSWORD@redis:6379/0"
fi

# À ADAPTER : secrets propres à l'application, par exemple
#   write api_token "$(rand)"

chmod 644 secrets/* 2>/dev/null || true
echo "==> OK. Ne jamais committer ni copier ce dossier hors du serveur."
