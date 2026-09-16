#!/usr/bin/env bash
#
# Who watches the bot.
#
# Every alert in this stack leaves through the bot, which means that when the
# bot dies the silence looks exactly like everything being fine. This script is
# the answer, and the one rule that makes it work is that it shares NOTHING
# with the bot: it is cron, psql and curl, and it talks to Telegram directly.
# If it needed the bot to deliver its message it would be useless precisely
# when it matters.
#
# What it covers: the bot crashed, hung, was OOM-killed, or lost the database.
# What it does not: this whole server being gone. Closing that needs a pinger
# somewhere else — set HEARTBEAT_PING_URL for the bot and point it at one.
#
# Install (every five minutes):
#   */5 * * * * /opt/portfolio/deploy/cron/bot-watchdog.sh >> /var/log/bot-watchdog.log 2>&1

set -euo pipefail

DEPLOY_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="$DEPLOY_DIR/docker-compose.prod.yml"
STATE_FILE="/tmp/.bot-watchdog-alerted"

# How old the pulse may be. The bot beats every two minutes, so ten leaves room
# for a slow round without crying wolf.
MAX_AGE_MINUTES=10
# Do not repeat the same alert more often than this.
REPEAT_AFTER_MINUTES=60

set -a
# shellcheck disable=SC1091
source "$DEPLOY_DIR/.env"
set +a

compose() { docker compose -f "$COMPOSE_FILE" --env-file "$DEPLOY_DIR/.env" "$@"; }

notify() {
  curl -sS --max-time 15 \
    "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage" \
    -d "chat_id=${TELEGRAM_ALLOWED_USER_ID}" \
    -d "parse_mode=HTML" \
    --data-urlencode "text=$1" >/dev/null
}

# A single question: is the pulse stale? "t", "f", or empty when the database
# itself cannot be reached — which is also worth hearing about.
stale="$(compose exec -T db psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tAc \
  "SELECT (now() - beat_at) > interval '$MAX_AGE_MINUTES minutes' FROM bot_heartbeat WHERE id = 1" \
  2>/dev/null | tr -d '[:space:]' || true)"

if [ "$stale" = "f" ]; then
  rm -f "$STATE_FILE"
  exit 0
fi

# Alert at most once an hour while the condition persists.
if [ -f "$STATE_FILE" ]; then
  last=$(cat "$STATE_FILE" 2>/dev/null || echo 0)
  now=$(date +%s)
  if [ $(( (now - last) / 60 )) -lt "$REPEAT_AFTER_MINUTES" ]; then
    exit 0
  fi
fi

# Real newlines, not %0A: curl --data-urlencode does the encoding, and a
# pre-encoded string would arrive in Telegram with the escapes showing.
if [ -z "$stale" ]; then
  message="$(printf '🚨 <b>Watchdog</b>\n\nBazaga ulanib bo'"'"'lmadi — bot ham, baza ham javob bermayapti.')"
else
  running="$(compose ps --services --status running 2>/dev/null | tr '\n' ' ' || echo '?')"
  message="$(printf '🚨 <b>Bot javob bermayapti</b>\n\nPuls %s daqiqadan beri yangilanmadi.\nIshlayotgan servislar: %s\n\n<code>docker compose logs --tail 50 bot</code>' "$MAX_AGE_MINUTES" "$running")"
fi

notify "$message"
date +%s > "$STATE_FILE"
