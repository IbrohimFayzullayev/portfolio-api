#!/usr/bin/env bash
#
# Nightly database dump, and the row that proves it happened.
#
# The row is the point. A backup that fails is invisible — nothing breaks, the
# site stays up, and you find out on the day you need it. The bot watches for
# the ABSENCE of these rows (see watchBackups): more than 26 hours without one
# and it says so.
#
# Off-site copy (optional, strongly recommended): a dump that lives on the same
# disk as the database protects against a bad migration and nothing else. Set
# BACKUP_REMOTE in .env to an rclone destination — "b2:my-bucket/portfolio",
# "s3:bucket/path", "sftp-host:backups" — and each dump is copied there after
# it is written. See README.md for the one-time rclone setup.
#
# Install (nightly at 03:30):
#   30 3 * * * /opt/portfolio/deploy/cron/backup.sh >> /var/log/backup.log 2>&1

set -euo pipefail

DEPLOY_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="$DEPLOY_DIR/docker-compose.prod.yml"
BACKUP_DIR="${BACKUP_DIR:-/var/backups/portfolio}"
KEEP_DAYS=14
# Kept longer than the local copies: remote storage is cheap, and the day the
# local disk is gone is the day an older dump may be the only clean one.
REMOTE_KEEP_DAYS="${BACKUP_REMOTE_KEEP_DAYS:-30}"

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

notify() { # kind, severity, jsonb expression
  compose exec -T db psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -c \
    "INSERT INTO notifications (kind, severity, payload) VALUES ('$1', $2, $3);" >/dev/null
}

# The off-site copy. "off" is a legitimate state and is reported as such, so
# the daily message says which kind of backup this was instead of implying
# more safety than there is.
offsite="off"
if [ -n "${BACKUP_REMOTE:-}" ]; then
  if ! command -v rclone >/dev/null 2>&1; then
    offsite="failed"
    offsite_error="rclone is not installed"
  elif out="$(rclone copy "$target" "$BACKUP_REMOTE" 2>&1)"; then
    offsite="ok"
    # Pruning is best-effort: failing to delete an old copy is not a failed
    # backup.
    rclone delete "$BACKUP_REMOTE" --include 'portfolio-*.sql.gz' \
      --min-age "${REMOTE_KEEP_DAYS}d" >/dev/null 2>&1 || true
  else
    offsite="failed"
    offsite_error="$(printf '%s' "$out" | tail -n 1 | tr -d "'" | cut -c1-200)"
  fi

  if [ "$offsite" = "failed" ]; then
    # Critical on purpose: the local dump succeeded, so nothing else will ever
    # complain, and "backups are fine" is exactly the belief this must break.
    notify 'backup.offsite_failed' 2 \
      "jsonb_build_object('file', '$(basename "$target")', 'error', '$offsite_error')" || true
  fi
fi

compose exec -T db psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -c \
  "INSERT INTO notifications (kind, payload) VALUES (
     'backup.finished',
     jsonb_build_object('size', '$size', 'duration', '${duration}s', 'file', '$(basename "$target")', 'offsite', '$offsite')
   );" >/dev/null
