# Portfolio API (Go)

A small, production-shaped REST API that powers the portfolio **dashboard** (admin
CMS). It manages blog posts and projects with JWT authentication.

## Stack

| Concern        | Choice                                |
| -------------- | ------------------------------------- |
| Language       | Go 1.23                               |
| Router         | chi v5 (+ chi/cors, chi/middleware)   |
| Database       | PostgreSQL 16                         |
| DB access      | pgx v5 + **sqlc** (type-safe SQL)     |
| Migrations     | golang-migrate                        |
| Auth           | JWT (HS256) + bcrypt                  |

## Prerequisites

Only two things:

- **Go 1.23+**
- **Docker Desktop** (to run PostgreSQL)

No other CLIs are needed:

- Database **migrations apply automatically on startup** (they're embedded in the
  binary — no `migrate` CLI).
- The `internal/db` data layer is **already generated and committed** — no `sqlc`
  needed to run. (You only need `sqlc` if you later change `db/queries/*.sql`.)

## Setup (local)

```bash
cp .env.example .env     # default values work out of the box

make setup               # starts PostgreSQL in Docker + go mod tidy
make run                 # starts the API on :8080 and runs migrations
```

That's it. On the first `make run` you'll see `applied 1 migration(s)` in the logs.

Prefer to do it by hand?

```bash
docker compose up -d db        # start PostgreSQL
go mod tidy                    # download Go modules
go run ./cmd/api               # run (auto-migrates, listens on :8080)
```

> **Where is the data?** The Docker volume `portfolio_pgdata` persists it across
> restarts. `make db-down` stops the container; `docker compose down -v` also
> wipes the data.

## Bootstrap the first admin

Registration is open only until the first user exists (single-author CMS):

```bash
curl -X POST http://localhost:8080/api/v1/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"email":"you@example.com","password":"supersecret","name":"Ibrohim"}'
```

The response includes a JWT `token`. Send it as `Authorization: Bearer <token>`.

## API

Base path: `/api/v1`

| Method | Path                    | Auth | Description                      |
| ------ | ----------------------- | ---- | -------------------------------- |
| GET    | `/healthz`              | —    | Health + DB ping                 |
| POST   | `/auth/register`        | —    | Create first admin (then closed) |
| POST   | `/auth/login`           | —    | Login → `{ token, user }`        |
| GET    | `/auth/me`              | ✓    | Current user                     |
| GET    | `/posts`                | ✓    | List (`?locale=`, `?status=`)    |
| POST   | `/posts`                | ✓    | Create                           |
| GET    | `/posts/{id}`           | ✓    | Get one                          |
| PUT    | `/posts/{id}`           | ✓    | Update                           |
| PATCH  | `/posts/{id}/publish`   | ✓    | `{ "draft": false }` toggle      |
| DELETE | `/posts/{id}`           | ✓    | Delete                           |
| GET    | `/projects`             | ✓    | List (`?locale=`, `?status=`)    |
| POST   | `/projects`             | ✓    | Create                           |
| GET    | `/projects/{id}`        | ✓    | Get one                          |
| PUT    | `/projects/{id}`        | ✓    | Update                           |
| DELETE | `/projects/{id}`        | ✓    | Delete                           |

`?status` accepts `draft`, `published`, or omit for all.

## Project layout

```
cmd/api/main.go            # entrypoint: connect, auto-migrate, graceful shutdown
internal/
├── config/                # env-based configuration
├── auth/                  # JWT issuing/parsing + bcrypt helpers
├── database/              # pgx pool + startup migration runner
├── db/                    # data layer (sqlc-shaped, committed)
└── api/                   # HTTP layer
    ├── server.go          # chi router + middleware wiring
    ├── middleware.go      # JWT auth middleware
    ├── response.go        # JSON helpers
    ├── dto.go             # request/response types + validation
    ├── errors.go          # pg error helpers
    ├── auth_handler.go
    ├── post_handler.go
    └── project_handler.go
db/
├── embed.go               # embeds the migrations into the binary
├── migrations/            # SQL migrations (auto-applied on startup)
└── queries/               # sqlc query definitions (source for internal/db)
```

## Connecting to the portfolio site

The site (Next.js) can later read **published** content from this API instead of
local MDX — pair it with the `revalidate` already set on the blog routes for ISR.
For now the API is consumed by the admin dashboard (`../portfolio-dashboard`).
