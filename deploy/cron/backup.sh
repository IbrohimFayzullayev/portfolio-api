#!/usr/bin/env bash
#
# Nightly database dump, and the row that proves it happened.
#
# The row is the point. A backup that fails is invisible — nothing breaks, the
# site stays up, and you find out on the day you need it. The bot watches for
# the ABSENCE of these rows (see watchBackups): more than 26 hours without one
# and it says so.
#
# Install (nightly at 03:30):
#   30 3 * * * /opt/portfolio/deploy/cron/backup.sh >> /var/log/backup.log 2>&1

set -euo pipefail

DEPLOY_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="$DEPLOY_DIR/docker-compose.prod.yml"
BACKUP_DIR="${BACKUP_DIR:-/var/backups/portfolio}"
KEEP_DAYS=14

set -a
# shellcheck disable=SC1091
source "$DEPLOY_DIR/.env"
set +a

mkdir -p "$BACKUP_DIR"
stamp="$(date +%Y-%m-%d-%H%M)"
target="$BACKUP_DIR/portfolio-$stamp.sql.gz"
started=$(date +%s)

compose() { docker compose -f "$COMPOSE_FILE" --env-file "$DEPLOY_DIR/.env" "$@"; }

# Dump to a temporary name and move it into place, so an interrupted run never
# leaves a half-written file that looks like a backup.
compose exec -T db pg_dump -U "$POSTGRES_USER" "$POSTGRES_DB" | gzip > "$target.partial"
mv "$target.partial" "$target"

duration=$(( $(date +%s) - started ))
size="$(du -h "$target" | cut -f1)"

find "$BACKUP_DIR" -name 'portfolio-*.sql.gz' -mtime "+$KEEP_DAYS" -delete

compose exec -T db psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -c \
  "INSERT INTO notifications (kind, payload) VALUES (
     'backup.finished',
     jsonb_build_object('size', '$size', 'duration', '${duration}s', 'file', '$(basename "$target")')
   );" >/dev/null
