#!/usr/bin/env bash
#
# Disk, memory and load, from the host into the bot's queue.
#
# The bot runs in a container and deliberately has no Docker socket and no view
# of the host — giving it one would make it root over the whole stack for the
# sake of three numbers. So the host writes the numbers itself, as a row in the
# same notifications table everything else uses. Same pattern the CI deploy job
# already uses for deploy.finished: no new privileges, no new delivery path.
#
# Install (every morning at 08:55, so it lands in the daily summary):
#   55 8 * * * /opt/portfolio/deploy/cron/host-metrics.sh >> /var/log/host-metrics.log 2>&1

set -euo pipefail

DEPLOY_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="$DEPLOY_DIR/docker-compose.prod.yml"

# Percentage of disk use that turns this from information into an alert.
DISK_ALERT_PERCENT=85

set -a
# shellcheck disable=SC1091
source "$DEPLOY_DIR/.env"
set +a

disk_line="$(df -h / | awk 'NR==2 {print $3 " / " $2 " (" $5 ")"}')"
disk_percent="$(df / | awk 'NR==2 {gsub("%","",$5); print $5}')"
memory_line="$(free -h | awk 'NR==2 {print $3 " / " $2}')"
load_line="$(uptime | sed 's/.*load average: //')"

kind="host.metrics"
severity=0
if [ "$disk_percent" -ge "$DISK_ALERT_PERCENT" ]; then
  # Same payload, louder: a full disk takes the database with it, and it is
  # the one host problem that gives plenty of warning if anyone is listening.
  kind="host.alert"
  severity=2
fi

docker compose -f "$COMPOSE_FILE" --env-file "$DEPLOY_DIR/.env" \
  exec -T db psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -v ON_ERROR_STOP=1 -c \
  "INSERT INTO notifications (kind, payload, severity) VALUES (
     '$kind',
     jsonb_build_object(
       'disk',   '$disk_line',
       'memory', '$memory_line',
       'load',   '$load_line'
     ),
     $severity
   );" >/dev/null
