# Host cron jobs

Three jobs that live on the server rather than in a container, each for the
same reason: they need something the bot deliberately does not have — a view of
the host, or independence from the bot itself.

```cron
# The bot's pulse. Talks to Telegram directly, so it still works when the bot
# does not.
*/5 * * * * /opt/portfolio/deploy/cron/bot-watchdog.sh >> /var/log/bot-watchdog.log 2>&1

# Disk, memory, load — written as a notifications row, picked up by the bot.
55 8 * * * /opt/portfolio/deploy/cron/host-metrics.sh >> /var/log/host-metrics.log 2>&1

# Nightly dump. The row it writes is what the bot watches for the absence of.
30 3 * * * /opt/portfolio/deploy/cron/backup.sh >> /var/log/backup.log 2>&1
```

All three read `deploy/.env` and reach PostgreSQL through
`docker compose exec db psql`. None of them needs the API or the bot to be
running.

**The gap.** The watchdog runs on the same machine as the bot, so it catches a
dead bot but not a dead server. To close that, set `HEARTBEAT_PING_URL` in
`.env` to a check URL from any external uptime service; the bot pings it every
two minutes, and that service alerts when the pings stop.

**Restore test.** The bot queues a reminder on the first of each month. A
backup nobody has restored is a file, not a backup:

```sh
gunzip -c /var/backups/portfolio/portfolio-YYYY-MM-DD-HHMM.sql.gz \
  | docker compose exec -T db psql -U "$POSTGRES_USER" -d portfolio_restore_test
```
