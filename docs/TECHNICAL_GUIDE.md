# MC Manager — Backend Technical Guide

> **Repo:** [`lomokwa/mc-manager-server`](https://github.com/lomokwa/mc-manager-server) · Go + [Gin](https://gin-gonic.com/) · SQLite ([`mattn/go-sqlite3`](https://github.com/mattn/go-sqlite3), CGO)
> **Sibling repo:** [`lomokwa/mc-manager-client`](https://github.com/lomokwa/mc-manager-client) — see its own [`docs/TECHNICAL_GUIDE.md`](https://github.com/lomokwa/mc-manager-client/blob/main/docs/TECHNICAL_GUIDE.md) for the frontend.
> **Português:** [`docs/GUIA_TECNICO.md`](GUIA_TECNICO.md)
>
> This guide describes the code as of commit [`ed608ed`](https://github.com/lomokwa/mc-manager-server/commit/ed608ed). Line-number links can drift as the file changes; if a link lands a few lines off, search the file for the function/symbol name given next to it — those don't drift.

## Table of contents

1. [System architecture](#1-system-architecture)
2. [Repository layout](#2-repository-layout)
3. [Boot sequence](#3-boot-sequence)
4. [Configuration (environment variables)](#4-configuration-environment-variables)
5. [Authentication & authorization](#5-authentication--authorization)
6. [HTTP API reference](#6-http-api-reference)
7. [Domain modules](#7-domain-modules)
8. [Data model](#8-data-model)
9. [Observability](#9-observability)
10. [The console protocol (WebSocket)](#10-the-console-protocol-websocket)
11. [Testing](#11-testing)
12. [CI/CD & deployment](#12-cicd--deployment)
13. [Frontend, in brief](#13-frontend-in-brief)
14. [Cookbook — "I want to…"](#14-cookbook--i-want-to)
15. [Glossary](#15-glossary)

---

## 1. System architecture

MC Manager runs as **two containers** sharing one bind-mounted directory (`./minecraft-server`) and nothing else — no network channel between them:

```
┌─────────────────────────┐        shared bind mount        ┌──────────────────────────┐
│   mc-manager (this repo)│◄────  ./minecraft-server  ────► │  minecraft (Dockerfile.   │
│   Go + Gin REST API      │        (world, logs, jar,       │  minecraft, this repo's   │
│   :8080                  │        server.properties,       │  cmd/supervisor)          │
│                          │        .mcmanager/ control      │  owns the JVM directly     │
└─────────────────────────┘        plane)                   └──────────────────────────┘
```

This split exists so that **redeploying the API never disconnects players**. `docker-compose.yml`'s deploy path only ever rebuilds `mc-manager` (`docker compose up -d --build --no-deps mc-manager` — see [§12](#12-cicd--deployment)); the `minecraft` container, and the JVM inside it, is untouched by a normal deploy.

Since the API has no direct handle on the JVM, the two containers talk through three files on the shared volume — the **control plane**, defined in [`services/constants.go`](../services/constants.go) and implemented by [`cmd/supervisor/main.go`](../cmd/supervisor/main.go):

| File | Direction | Purpose |
|---|---|---|
| `.mcmanager/console.in` (FIFO) | API → supervisor | Raw console commands, forwarded verbatim to the JVM's stdin |
| `.mcmanager/control.in` (FIFO) | API → supervisor | Lifecycle verbs: `START`, `STOP`, `RESTART`, `KILL` |
| `.mcmanager/status.json` | supervisor → API | Atomically-replaced heartbeat: `running` / `pid` / `since` / `heartbeat` / `desired` |

The API never reads the JVM's stdout directly either — Minecraft already writes `logs/latest.log`, and the API tails that file itself ([`services/logtail.go`](../services/logtail.go)) rather than depend on the supervisor for log delivery. Two independent channels, two independent failure modes, easier to reason about than one.

**Multi-server note.** As of this commit, the `servers` table (see [§8](#8-data-model)) and the `ServerRuntime` abstraction ([§7.1](#71-server-lifecycle--the-multi-server-registry)) support *describing* more than one server, but only one JVM actually runs — there is no `POST /api/servers` yet, and `cmd/supervisor` only ever manages a single process. Treat the registry as "Phase 1 of a multi-server rollout," not "multi-server is live."

## 2. Repository layout

```
main.go                    Entry point: boot sequence + full route table (newRouter)
logging.go                 slog setup (JSON in prod, text when GIN_MODE=debug)
main_routes_test.go        End-to-end router tests (flat vs. namespaced routes)
main_redaction_test.go     Locks down the access-log credential redaction

cmd/supervisor/main.go     The OTHER binary. Runs inside the "minecraft" container,
                            owns the JVM, speaks the control-plane protocol above.
                            Linux-only (//go:build linux).

handlers/                  One file per REST resource. Each function is a
                            gin.HandlerFunc: parse input, call a services/
                            function, shape the JSON response. No business
                            logic lives here.
middleware/                Cross-cutting gin.HandlerFunc: JWT/API-key auth,
                            permission checks, rate limiting, :sid resolution.
services/                  All business logic. Talks to the filesystem, the
                            control-plane FIFOs, and the database.
types/                     Shared structs: DB row shapes, API request/response
                            bodies, the permission enum.
db/                        db.go opens the SQLite file and runs migrations.sql
                            on every boot (all statements are IF NOT EXISTS —
                            see §8).
utils/                     Small stateless helpers (currently just file.go).
docs/                      This guide, its PT-BR twin, and docs.go (generated
                            Swagger annotations — see §6).
```

Everything under `web/` is a **stray, untracked leftover** from a rejected standalone-frontend experiment. It is not part of the running system, is not referenced by any Dockerfile, and should not be treated as documentation of anything.

## 3. Boot sequence

Read top-to-bottom, [`main.go`](../main.go)'s `main()` does exactly this, in order:

1. **[`setupLogging()`](../logging.go)** — installs the process-wide `slog` handler. Must be first: everything after this point can log.
2. **`godotenv.Load()`** — best-effort `.env` load; a missing file is not an error (container deploys inject env vars directly).
3. **pprof listener** — starts in a goroutine, bound to `127.0.0.1:6060` only. See [§9](#9-observability).
4. **`db.Init(os.Getenv("DB_PATH"))`** — opens SQLite, runs [`db/migrations.sql`](../db/migrations.sql).
5. **[`services.EnsureBuiltinRoles()`](../services/permissions.go#L18)** — seeds the five built-in roles (Owner/Admin/Moderator/Operator/Viewer). Fatal on error.
6. **[`services.ApplyPermissionsSeed()`](../services/seed.go#L45)** — reads `permissions-seed.json` if present and assigns roles to matching usernames. Runs every boot; a no-op for anyone already assigned. See [§5.2](#52-permissions--the-rbac-model).
7. **[`services.EnsureBootstrapOwner()`](../services/seed.go#L83)** — safety net: if *nobody* has a role after step 6 (missing seed file, or its usernames haven't registered yet), promotes the first registered account to Owner. Without this, a fresh deploy with no seed file would deny-by-default *everyone*, permanently, with no way back in through the UI.
8. **[`services.EnsureDefaultServer()`](../services/servers.go#L28)** — idempotent: seeds the `servers` table with one row pointing at the pre-existing `ServerDir`, if the table is empty. Fatal on error.
9. **[`services.LoadRuntimes()`](../services/runtime.go#L110)** — builds the in-memory `map[string]*ServerRuntime` from the `servers` table and starts each runtime's log tailer. Must run before any handler is reachable.
10. **[`services.StartBackupScheduler()`](../services/backup_scheduler.go#L15)** — starts the goroutine that runs scheduled backups per `backup_config`.
11. **`gin.SetMode(...)`** — `GIN_MODE` env var, defaults to `release`.
12. **`newRouter().Run()`** — builds the full route table (see [§6](#6-http-api-reference)) and blocks, serving HTTP.

Steps 5, 8 and 9 call a local `fatal()` helper ([`logging.go`](../logging.go)) on error — it logs at `ERROR` through the structured handler and calls `os.Exit(1)`, so a boot failure is as queryable in your log aggregator as anything else the process emits.

## 4. Configuration (environment variables)

| Variable | Required | Default | Used by |
|---|---|---|---|
| `DB_PATH` | yes | — | [`db.Init`](../db/db.go) — path to the SQLite file |
| `JWT_SECRET` | yes | — | [`middleware/auth.go`](../middleware/auth.go) — HMAC signing key for login tokens |
| `API_KEY` | for the admin route | — | [`middleware/auth.go`](../middleware/auth.go) — `X-API-Key` / `?key=` auth, constant-time compared |
| `CORS_ALLOWED_ORIGINS` | no | `http://localhost:5173,http://localhost:8080` | [`main.go` `allowedOrigins()`](../main.go) — comma-separated; also gates the console WebSocket's `Origin` check (CSWSH guard, [`handlers/console.go`](../handlers/console.go)) |
| `GIN_MODE` | no | `release` | [`main.go`](../main.go) — `debug` also switches the log handler to text (see [§9](#9-observability)) |
| `LOG_LEVEL` | no | `info` | [`logging.go`](../logging.go) — `debug`\|`info`\|`warn`\|`error`; an unrecognised value silently falls back to `info` rather than refusing to boot |
| `PORT` | no | `8080` (gin default) | the HTTP listener |
| `CLIENT_URL` | situational | — | referenced by invitation/link flows that build a URL back to the frontend |

Container-only variables (read by `cmd/supervisor`, not by the API): `MC_SERVER_DIR` (default `/mc`), `MC_SERVER_JAR` (default `server.jar`), `MC_JAVA_XMS`/`MC_JAVA_XMX` (default `1G`/`2G`), `MC_STOP_TIMEOUT` (default `30s`) — all read in [`cmd/supervisor/main.go`](../cmd/supervisor/main.go).

## 5. Authentication & authorization

Three independent layers, applied in this order for a typical request: **auth** (who are you) → **permission** (may you do this) → **server resolution** (which server, for namespaced routes).

### 5.1 Auth middleware

| Middleware | File | Accepts | Used on |
|---|---|---|---|
| `ValidateJWT()` | [`middleware/auth.go`](../middleware/auth.go) | `Authorization: Bearer <jwt>` header, or `?token=` query param (the console WebSocket needs this — browsers can't set headers on a WS handshake) | the `api` route group (almost everything) |
| `ValidateAPIKey()` | [`middleware/auth.go`](../middleware/auth.go) | `X-API-Key` header or `?key=` query param, compared with [`crypto/subtle.ConstantTimeCompare`](https://pkg.go.dev/crypto/subtle#ConstantTimeCompare) | not mounted alone; folded into the next one |
| `ValidateAPIKeyOrJWT()` | [`middleware/auth.go`](../middleware/auth.go) | either of the above | the `admin` group (`POST /api/admin/invitations`) — lets a script create invitations with just the API key, no login |

JWT claims set at login ([`services/users.go` `Login`](../services/users.go#L93)): `user_id` (float64 once decoded — JSON numbers), `username`, `exp` (24h). Read back via [`middleware.UserIDFromContext`](../middleware/permissions.go#L14) and [`middleware.UsernameFromContext`](../middleware/permissions.go).

Both `key` and `token` query params are stripped from access logs — see [§9](#9-observability).

### 5.2 Permissions — the RBAC model

**Files:** [`types/permissions.go`](../types/permissions.go) (the enum + schema + built-in roles), [`services/permissions.go`](../services/permissions.go) (resolution + mutation), [`middleware/permissions.go`](../middleware/permissions.go) (the gin middleware).

A `Permission` is a stable string (`"server.start"`, `"files.delete"`, …), stored as JSON in two places:

- `roles.permissions` — a role's default set
- `user_roles.overrides` — a `map[Permission]bool` layered **on top of** the role's defaults for one specific user

[`EffectivePermissions(userID)`](../services/permissions.go#L46) computes the final answer: start from the role's list (all `true`), then apply overrides (`true` adds, `false` explicitly revokes something the role would otherwise grant). **A user with no `user_roles` row has zero permissions — deny by default.** This is not an error state; it's the default for every newly-registered account until an admin assigns a role.

`RequirePermission(perm)` ([`middleware/permissions.go`](../middleware/permissions.go)) is the gin middleware every gated route uses; in `main.go` it's aliased to `perm` for brevity (`perm(types.PermServerStart)`). It 403s with the missing permission named in the error body — the client's `apiFetch` (see the frontend guide) surfaces that message directly rather than a generic "forbidden".

**Permission reference** — every value in [`types/permissions.go`](../types/permissions.go)'s `PermissionSchema`, grouped by zone (this is also exactly what `GET /api/permissions/schema` returns, and what the client's role editor renders):

| Zone | Permission | Grants |
|---|---|---|
| Server control | `server.start` | Boot the Minecraft server |
| | `server.stop` | Shut it down |
| Console | `console.read` | View the live console feed |
| | `console.chat` | Broadcast a chat message (`say`) |
| | `console.commands` | Run any other console command |
| Files | `files.read` | Browse & download |
| | `files.upload` | Add new files |
| | `files.edit` | Change existing file contents |
| | `files.delete` | Remove files/folders |
| Backups | `backups.view` | See the list & schedule |
| | `backups.create` | Manual backup + change the schedule |
| | `backups.download` | Download an archive |
| | `backups.delete` | Remove an archive |
| | `backups.restore` | Replace the live world — the most destructive one |
| Server settings | `settings.view` | See `server.properties` |
| | `settings.edit` | Change `server.properties` |
| Performance | `performance.view` | TPS/memory/CPU |
| | `performance.report` | Run spark profiler/health reports |
| Players | `players.view` | Roster & profiles |
| | `players.moderate` | Op/de-op, kick, ban, whitelist |
| Administration | `admin.manage_users` | Invite/remove website accounts |
| | `admin.manage_roles` | Assign roles, edit per-user overrides |

**Built-in roles** ([`types.BuiltinRoles`](../types/permissions.go#L158), seeded by [`EnsureBuiltinRoles`](../services/permissions.go#L18) on every boot):

| Role | Permissions |
|---|---|
| **Owner** | Every permission that exists. Cannot be assigned/edited through the API or UI — only via the seed file (see below). There is exactly one path to unrestricted access, and it isn't a UI button. |
| **Admin** | Every permission that exists, same as Owner, but *is* assignable through the UI. |
| **Moderator** | `console.read`, `console.chat`, `console.commands`, `players.view`, `players.moderate` |
| **Operator** | `server.start`, `server.stop`, `console.read`, `console.chat`, `players.view` |
| **Viewer** | `console.read`, `performance.view`, `players.view` |

**The permissions seed file** ([`services/seed.go`](../services/seed.go)) — an operator drops `permissions-seed.json` next to `server.properties` (or in the repo root as a fallback) *before* the first deploy, to pre-assign roles without hardcoding usernames into the codebase:

```json
[
  { "username": "lomokwa", "role": "Owner" },
  { "username": "Ant", "role": "Admin" }
]
```

`ApplyPermissionsSeed` runs on **every** boot, not just the first — a username that hasn't registered yet is retried on the next restart, and an existing assignment is never overwritten (so it's safe to leave the file in place permanently, or fix a typo in it and restart).

### 5.3 Server resolution (`:sid`)

`ResolveServer()` ([`middleware/server.go`](../middleware/server.go)) mounts on the `/api/servers/:sid` group. It looks up `:sid` in the registry, 404s a request has an unknown id (before any handler runs), and stores the matching `*services.ServerRuntime` in the gin context. Handlers read it back with [`runtimeFromRequest(c)`](../handlers/runtime.go) — every handler that touches "the server" goes through this, whether it was reached via the flat route (which resolves to the default runtime) or a namespaced one.

## 6. HTTP API reference

Built in [`main.go`](../main.go)'s `newRouter()`. Two families cover the same handlers:

- **Flat routes** (`/api/players`, `/api/start`, …) — always resolve to the *default* server. These are **permanent**, not deprecated: [selton-mello-bot](https://github.com/lomokwa/selton-mello-bot) (a separately-deployed Discord bot) calls `/api/players` with no way to learn about namespaced routes on its own schedule. Breaking these breaks production. See `TestFlatRoutes_StillRouteAndMatchDefaultServer` in [`main_routes_test.go`](../main_routes_test.go).
- **Namespaced routes** (`/api/servers/:sid/players`, …) — the *same* handler, resolved against whichever server `:sid` names.

Auth column: 🔓 public · 🔑 JWT (any logged-in user) · 🔑+`perm` JWT with the named permission · 🗝️ API key or JWT.

| Method | Flat path | Namespaced path | Auth | Handler |
|---|---|---|---|---|
| POST | `/api/register` | — | 🔓 | [`RegisterHandler`](../handlers/users.go#L37) |
| POST | `/api/login` | — | 🔓 | [`LoginHandler`](../handlers/users.go#L78) |
| GET | `/api/invitations/:token` | — | 🔓 | [`ValidateInvitationHandler`](../handlers/users.go#L26) |
| POST | `/api/admin/invitations` | — | 🗝️ | [`CreateInvitationHandler`](../handlers/users.go#L13) |
| GET | `/api/me` | — | 🔑 | [`GetMeHandler`](../handlers/users.go#L54) |
| GET | `/api/users` | — | 🔑+`admin.manage_users` | [`GetUsersHandler`](../handlers/users.go#L68) |
| GET | `/api/permissions/schema` | — | 🔑 | [`PermissionSchemaHandler`](../handlers/roles.go#L22) |
| GET | `/api/me/permissions` | — | 🔑 | [`MyPermissionsHandler`](../handlers/roles.go#L29) |
| GET | `/api/roles` | — | 🔑+`admin.manage_roles` | [`ListRolesHandler`](../handlers/roles.go#L48) |
| GET | `/api/users/:id/permissions` | — | 🔑+`admin.manage_roles` | [`GetUserPermissionsHandler`](../handlers/roles.go#L59) |
| PUT | `/api/users/:id/role` | — | 🔑+`admin.manage_roles` | [`SetUserRoleHandler`](../handlers/roles.go#L92) |
| PUT | `/api/users/:id/overrides` | — | 🔑+`admin.manage_roles` | [`SetUserOverridesHandler`](../handlers/roles.go#L141) |
| GET | `/api/me/mclink` | — | 🔑 | [`GetMcLinkHandler`](../handlers/mclink.go#L178) |
| POST | `/api/me/mclink/start` | — | 🔑 | [`StartMcLinkHandler`](../handlers/mclink.go#L66) |
| POST | `/api/me/mclink/verify` | — | 🔑 | [`VerifyMcLinkHandler`](../handlers/mclink.go#L127) |
| DELETE | `/api/me/mclink` | — | 🔑 | [`UnlinkMcHandler`](../handlers/mclink.go#L205) |
| POST | `/api/server` | — | 🔑+`server.start` | [`CreateServerHandler`](../handlers/server.go#L24) |
| GET | `/api/server` | — | 🔑 | [`ServerExistsHandler`](../handlers/server.go#L149) |
| DELETE | `/api/server` | — | 🔑+`server.stop` | [`DeleteServerHandler`](../handlers/server.go#L119) |
| POST | `/api/start` | `/api/servers/:sid/start` | 🔑+`server.start` | [`StartServerHandler`](../handlers/server.go#L90) |
| POST | `/api/stop` | `/api/servers/:sid/stop` | 🔑+`server.stop` | [`StopServerHandler`](../handlers/server.go#L171) |
| GET | `/api/status` | `/api/servers/:sid/status` | 🔑 | [`StatusHandler`](../handlers/server.go#L192) |
| GET | `/api/console` (WS) | `/api/servers/:sid/console` | 🔑+`console.read` | [`ConsoleHandler`](../handlers/console.go#L71) — see [§10](#10-the-console-protocol-websocket) |
| GET | `/api/players` | `/api/servers/:sid/players` | 🔑+`players.view` | [`ListPlayersHandler`](../handlers/players.go#L11) |
| GET | `/api/properties` | `/api/servers/:sid/properties` | 🔑+`settings.view` | [`GetServerPropertiesHandler`](../handlers/properties.go#L8) |
| PATCH | `/api/properties` | `/api/servers/:sid/properties` | 🔑+`settings.edit` | [`UpdateServerPropertiesHandler`](../handlers/properties.go#L19) |
| GET | `/api/files` | `/api/servers/:sid/files` | 🔑+`files.read` | [`ListFilesHandler`](../handlers/files.go#L51) |
| GET | `/api/files/read` | `/api/servers/:sid/files/read` | 🔑+`files.read` | [`ReadFileHandler`](../handlers/files.go#L94) |
| PUT | `/api/files` | `/api/servers/:sid/files` | 🔑+`files.edit` | [`WriteFileHandler`](../handlers/files.go#L129) |
| GET | `/api/files/download` | `/api/servers/:sid/files/download` | 🔑+`files.read` | [`DownloadFileHandler`](../handlers/files.go#L165) |
| POST | `/api/files/upload` | `/api/servers/:sid/files/upload` | 🔑+`files.upload` | [`UploadFileHandler`](../handlers/files.go#L188) |
| DELETE | `/api/files` | `/api/servers/:sid/files` | 🔑+`files.delete` | [`DeleteFileHandler`](../handlers/files.go#L239) |
| GET | `/api/backups` | `/api/servers/:sid/backups` | 🔑+`backups.view` | [`ListBackupsHandler`](../handlers/backups.go#L21) |
| POST | `/api/backups` | `/api/servers/:sid/backups` | 🔑+`backups.create` | [`CreateBackupHandler`](../handlers/backups.go#L42) |
| DELETE | `/api/backups` | `/api/servers/:sid/backups` | 🔑+`backups.delete` | [`DeleteBackupHandler`](../handlers/backups.go#L66) |
| GET | `/api/backups/download` | `/api/servers/:sid/backups/download` | 🔑+`backups.download` | [`DownloadBackupHandler`](../handlers/backups.go#L94) |
| POST | `/api/backups/restore` | `/api/servers/:sid/backups/restore` | 🔑+`backups.restore` | [`RestoreBackupHandler`](../handlers/backups.go#L125) |
| GET | `/api/backups/config` | `/api/servers/:sid/backups/config` | 🔑+`backups.view` | [`GetBackupConfigHandler`](../handlers/backups.go#L165) |
| PUT | `/api/backups/config` | `/api/servers/:sid/backups/config` | 🔑+`backups.create` | [`UpdateBackupConfigHandler`](../handlers/backups.go#L187) |
| GET | `/api/servers` | — | 🔑 | [`ListServersHandler`](../handlers/servers.go#L61) |
| GET | `/api/servers/:sid` | — | 🔑 | [`GetServerHandler`](../handlers/servers.go#L98) |
| GET | `/api/docs/*any` | — | 🔓 | Swagger UI, see below |

**Swagger.** Every handler carries `@Summary`/`@Router` annotations consumed by [`swaggo/swag`](https://github.com/swaggo/swag) (`go generate` at the top of `main.go` regenerates [`docs/docs.go`](docs.go)); browse the live, always-current version at `/api/docs/index.html` on any running deployment rather than trusting this table's descriptions to stay perfectly in sync.

**Response envelope** — every handler replies with [`types.APIResponse`](../types/response.go): `{"success": bool, "data"?: T, "error"?: string}`. The frontend's `apiFetch` (see the client guide) is written specifically against this shape.

## 7. Domain modules

### 7.1 Server lifecycle & the multi-server registry

**Files:** [`services/runtime.go`](../services/runtime.go) (the `ServerRuntime` type + registry loading), [`services/servers.go`](../services/servers.go) (CRUD-ish helpers over the `servers` table), [`services/process.go`](../services/process.go) (start/stop/status via the control plane), [`services/minecraft.go`](../services/minecraft.go) (jar download, `server.properties`, player list).

`ServerRuntime` ([`services/runtime.go`](../services/runtime.go#L17)) holds everything that used to be a package-level global before the multi-server registry existed: one `*types.LogHub`, one console-write mutex, one backup mutex, one online-players cache — all scoped to a single server directory (`Dir`). It's always used as a pointer. [`DefaultRuntime()`](../services/runtime.go#L130) and [`RuntimeForID(id)`](../services/runtime.go#L146) are the two ways to get one; every handler goes through [`runtimeFromRequest`](../handlers/runtime.go) instead of calling these directly, so flat and namespaced routes share one code path.

Path derivation ([`ControlDir`](../services/runtime.go#L53), `ConsoleFifoPath`, `LatestLogPath`, etc.) deliberately uses **string concatenation, not `filepath.Join`** — for the default runtime, whose `Dir` is exactly the `ServerDir` constant, every method must produce a result byte-identical to its `constants.go` counterpart (`filepath.Join` would silently clean away the leading `./`). See `TestDefaultRuntime_MatchesExistingConstants` in [`services/runtime_test.go`](../services/runtime_test.go).

`StartServerProcess`/`StopServerProcess` ([`services/process.go`](../services/process.go)) don't spawn a process directly — they write a lifecycle verb to `control.in` and poll `status.json` (via [`ReadStatus`](../services/process.go#L38)) until the supervisor reports the new state. `IsServerRunning()` treats a heartbeat older than 10 seconds as "not running", even if the last-written `running: true` — a wedged supervisor reads as down rather than as a false "up".

### 7.2 Console streaming

**Files:** [`services/logtail.go`](../services/logtail.go) (tails `latest.log`), [`types/hub.go`](../types/hub.go) (`LogHub` — the pub/sub fan-out), [`handlers/console.go`](../handlers/console.go) (the WebSocket endpoint, see [§10](#10-the-console-protocol-websocket)).

Each `ServerRuntime` gets its own tailer goroutine (`tailLoopFor`), started once via `sync.Once` when [`LoadRuntimes`](../services/runtime.go#L110) boots. It detects log rotation two ways — file identity via `os.SameFile`, or the file shrinking under the read offset — either one means "reopen from the top," since Minecraft's own readiness line must never be missed after a restart.

On (re)connect, a fresh subscriber gets the **backlog**: up to 200 of the file's most recent complete lines ([`readBacklog`](../services/logtail.go#L136), reading at most a 256 KiB window), so the console is never blank right after an API restart even though the JVM has been running the whole time.

### 7.3 Players

**File:** [`services/minecraft.go`](../services/minecraft.go) `ListPlayers` ([line 319](../services/minecraft.go#L319)).

Reads three static files under the server directory (`usercache.json`, `ops.json`, `banned-players.json`, `whitelist.json`) and cross-references them with a live `list` console command (`GetOnlinePlayers`, [line 249](../services/minecraft.go#L249), cached briefly per-runtime to avoid hammering the console on every poll). A missing optional file (ops/banned/whitelist) degrades to "nobody in that category" rather than an error; `usercache.json` itself is required (no player has ever joined = nothing to list).

### 7.4 Backups

**Files:** [`services/backup.go`](../services/backup.go) (create/restore/delete/prune, all `*ServerRuntime` methods), [`services/backup_scheduler.go`](../services/backup_scheduler.go) (the scheduled-backup goroutine).

`CreateBackup` zips the world directory after asking the running server to `save-off` / `save-all flush` / `save-on` (so the zip is internally consistent even while players are online); `RestoreBackup` refuses while the server is running (see `PermBackupsRestore`'s description — this is the single most destructive action in the system) and unzips with zip-slip path-traversal guards. `PruneBackups(keep)` deletes the oldest beyond `keep`, called after every scheduled backup per `backup_config.keep`.

`NotifyBackupConfigChanged()` ([`services/backup_scheduler.go` line 21](../services/backup_scheduler.go#L21)) is how `UpdateBackupConfigHandler` wakes the scheduler goroutine immediately after a config change, instead of waiting up to a full interval for it to notice.

### 7.5 File manager

**File:** [`handlers/files.go`](../handlers/files.go).

Every path is resolved through `safePath` (same file, unexported) before touching disk: it rejects `..` traversal and refuses any path inside the reserved `.mcmanager` control directory, both under the server's *own* runtime — see `TestSafePath_TraversalDeniedUnderArbitraryRuntime` in [`handlers/files_test.go`](../handlers/files_test.go) for why "arbitrary runtime" is asserted explicitly, not just the default one.

### 7.6 Minecraft account linking

**File:** [`handlers/mclink.go`](../handlers/mclink.go) + [`services`](../services/) helpers.

A website account proves it owns a Minecraft username by receiving a one-time code **in-game** (via `tellraw`, over the console connection) and confirming it in the web UI within a short expiry window (`mc_link_codes` table). One pending attempt per user; starting a new one overwrites whatever was pending. Verified links live in `minecraft_links`.

### 7.7 Users, invitations, login

**Files:** [`handlers/users.go`](../handlers/users.go), [`services/users.go`](../services/users.go).

Registration is invite-only: `CreateInvitation` mints a random token with an expiry, `Register` consumes it (marks `used_at`, refuses a token twice). Passwords are hashed with bcrypt (see `Login`/`Register` in [`services/users.go`](../services/users.go)); `Login` mints the JWT described in [§5.1](#51-auth-middleware).

## 8. Data model

Schema lives entirely in [`db/migrations.sql`](../db/migrations.sql), applied via a single `Exec` of the whole file on every boot ([`db/db.go`](../db/db.go)). Every statement is `CREATE TABLE IF NOT EXISTS` — **there are no numbered/versioned migrations**; a schema change here is additive-only, by convention, because this runs against a live production database on every deploy with no separate migration step.

| Table | Purpose | Key columns |
|---|---|---|
| `users` | Website accounts | `username` unique, `password_hash` (bcrypt) |
| `invitations` | Invite-only registration tokens | `token` unique, `expires_at`, `used_at` |
| `backup_config` | Single-row (`id=1`) backup schedule | `enabled`, `interval_minutes`, `keep` |
| `roles` | Named permission bundles | `permissions` (JSON array), `is_system` |
| `user_roles` | One row per user-with-a-role | `role_id`, `overrides` (JSON map) — **absence = deny-by-default** |
| `minecraft_links` | Verified account links | `mc_username`, `mc_uuid` |
| `mc_link_codes` | Pending link attempts | `code`, `expires_at` — one per user |
| `servers` | The multi-server registry ([§7.1](#71-server-lifecycle--the-multi-server-registry)) | `id`, `dir` unique, `port`, `xms`/`xmx` |

`roles.permissions` and `user_roles.overrides` store `Permission` values as JSON — see [§5.2](#52-permissions--the-rbac-model) for how they combine.

## 9. Observability

**Structured logging** ([`logging.go`](../logging.go)): every log line goes through `log/slog`. `GIN_MODE=debug` selects a human-readable text handler; anything else (i.e. production) selects JSON, which is what the homelab's Promtail/Loki stack ingests. `LOG_LEVEL` picks the minimum level, defaulting to `info`.

**The access logger** (`requestLogger()` in [`main.go`](../main.go#L282)) is custom, not gin's default — the **HTTP status picks the slog level**: 5xx → `ERROR`, 4xx → `WARN`, else `INFO`. This matters operationally: a wave of `403`s (e.g. an account that lost its role after a permissions change) now shows up as a wave of `WARN` lines instead of blending into ordinary traffic. Every access-log line carries `status`, `method`, `path`, `latency_ms`, `ip`.

**Credential redaction.** The `key` and `token` query params — both of which genuinely travel in URLs (the admin API key, and the console WebSocket's JWT, since browsers can't set headers on a WS handshake) — are stripped from the logged path by `credentialParamRe` ([`main.go` line 277](../main.go#L277)) before the line is ever written. Locked down by [`main_redaction_test.go`](../main_redaction_test.go).

**Healthcheck.** `./server healthcheck` (the same binary, a special first-argument branch checked *before* `setupLogging` or any of the boot sequence) makes an HTTP GET against its own `/healthz` and exits 0/1. This is what `docker-compose.yml`'s `mc-manager` service healthcheck runs — no `curl`/`wget` needed in the runtime image. `/healthz` is deliberately unauthenticated (a probe can't carry a JWT before anyone has logged in) and answers nothing but `{"status":"ok"}`.

**pprof.** `net/http/pprof` is registered on a *separate* listener bound to `127.0.0.1:6060` (see [`main.go` line 33](../main.go#L33)) — never joins the public Gin router, reachable only via `docker exec` from inside the container.

## 10. The console protocol (WebSocket)

Endpoint: `GET /api/console` (flat) or `GET /api/servers/:sid/console` (namespaced), upgraded from HTTP by [`ConsoleHandler`](../handlers/console.go#L71).

- **Auth:** JWT via `?token=` (see [§5.1](#51-auth-middleware)) plus `console.read` permission, checked before the upgrade.
- **CSWSH guard:** the `Upgrader.CheckOrigin` callback allows an empty `Origin` (non-browser clients — the Discord bot, scripts), a same-host `Origin`, or one listed in `CORS_ALLOWED_ORIGINS`. Everything else is rejected before the handshake completes.
- **Server → client:** every complete line the tailer reads from `latest.log` is broadcast verbatim as a text frame, plus the backlog burst on connect (see [§7.2](#72-console-streaming)).
- **Client → server:** a text frame is treated as a raw console command. [`classifyConsoleInput`](../handlers/console.go#L216) decides which permission it needs — a line starting with `say ` (case-sensitive, matching the actual command) needs `console.chat`; anything else needs `console.commands`. A command the caller isn't allowed to send never reaches the JVM; the socket gets a JSON error frame instead (`{"error": "..."}`).
- **Liveness:** a 30s ping/pong cycle plus read/write deadlines close connections whose peer vanished without a clean TCP FIN (a dead NAT/proxy), so the goroutine + hub subscription don't leak forever.
- **Server-stop signal:** a 2s status poll inside the same handler closes the socket with a normal-closure frame the moment `IsServerRunning()` goes false, since the hub itself is long-lived (it outlives any single JVM run) and no longer closes on stop the way the pre-multi-container version did.

## 11. Testing

`go test ./... -race -cover` is what CI runs (see [§12](#12-cicd--deployment)) — always run with `-race` locally too if you're touching anything concurrent (the log tailer, the console handler, the backup scheduler all have goroutines).

Conventions worth knowing before you add a test:

- Every package with tests defines its own `setupTestDB(t)` / `setupServerDir(t)` helpers (grep for them — they're duplicated per-package on purpose, not imported from a shared location, so each package's test suite has no non-test-file dependency on another package's internals).
- `main_routes_test.go` builds the **real** `*gin.Engine` via `newRouter()` and drives it with `httptest` — this is what proves the flat/namespaced route pairs actually behave identically, not just that each handler works in isolation.
- 3 tests are known to fail on native Windows and pass on CI's Linux runners: `TestSafePath_ValidPathResolves` (path-separator literal), `TestRotated_DifferentFileDetected` and `TestRotated_MissingPathIsNotRotated` (both `os.Remove` an open file, which Windows refuses). Don't chase these locally.
- `gofmt -l .` on a Windows checkout with `core.autocrlf=true` flags nearly every file as a false positive (CRLF, not real formatting). To check formatting for real on Windows, normalise line endings into a temp copy first, then run `gofmt` there.

## 12. CI/CD & deployment

**CI** ([`.github/workflows/ci.yml`](../.github/workflows/ci.yml)) on every push/PR to `main`: `gofmt -l` (must be empty), `go vet`, `go build`, `go test -race -cover`, then [`govulncheck`](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck). Any one of these failing is a **repo-wide** red X — including on PRs whose diff has nothing to do with the failure (the `govulncheck` step in particular can go red purely because a new CVE was published against the pinned Go toolchain, with zero code changes).

**Deploy** ([`.github/workflows/deploy.yml`](../.github/workflows/deploy.yml)) is a *separate* workflow, triggered by `workflow_run` **only after CI reports `success`** on `main`. Concretely: `git pull && docker compose up -d --build --no-deps mc-manager` over SSH (via `cloudflared access ssh`) on the homelab host. `--no-deps` is the whole point — it rebuilds only the `mc-manager` image, never touches the `minecraft` service or the JVM inside it.

**Consequence worth internalising:** a red CI on `main` means deploys silently stop happening — there's no separate alert, the merge just doesn't ship. Always check CI is green on `main` before assuming a merged PR is live.

**What can never auto-deploy.** Anything that only takes effect on a *recreated* `minecraft` container (a `docker-compose.yml` change to that service, a `cmd/supervisor` code change, `Dockerfile.minecraft`) needs a manually-run `docker compose up -d --build minecraft` on the host — which **does** restart the JVM and disconnect players. Plan that as its own step, deliberately, separate from a normal merge.

## 13. Frontend, in brief

The web UI is a separate repository, [`lomokwa/mc-manager-client`](https://github.com/lomokwa/mc-manager-client) (React 19 + TypeScript + Vite), talking to this API exclusively over HTTP/JSON and the console WebSocket described in [§10](#10-the-console-protocol-websocket). It has no other coupling to this codebase — no shared types package, no generated client; `types/response.go`'s `{success, data, error}` shape and the routes in [§6](#6-http-api-reference) are the entire contract. See its own [`docs/TECHNICAL_GUIDE.md`](https://github.com/lomokwa/mc-manager-client/blob/main/docs/TECHNICAL_GUIDE.md) for the frontend's architecture, page/route table, and state-management model.

## 14. Cookbook — "I want to…"

| I want to… | Start here |
|---|---|
| Add a new REST endpoint | Write the handler in `handlers/`, register it in `newRouter()` ([`main.go`](../main.go)) — both the flat *and* namespaced form if it's per-server, gated with `perm(types.PermX)` |
| Add a new permission | Add the `Permission` const + a `PermissionInfo` entry in the right zone in [`types/permissions.go`](../types/permissions.go); decide which `BuiltinRoles` should have it by default |
| Change what a role can do by default | [`types.BuiltinRoles`](../types/permissions.go#L158) — remember `EnsureBuiltinRoles` only *inserts*, it does not update an existing row, so a change here needs a real migration path to reach already-deployed databases, not just a code edit |
| Pre-assign roles on a fresh deploy | Drop a `permissions-seed.json` next to `server.properties` — see [§5.2](#52-permissions--the-rbac-model) |
| Add a field to the server registry | `servers` table in [`db/migrations.sql`](../db/migrations.sql) + [`types/server.go`](../types/server.go) + [`services/servers.go`](../services/servers.go) |
| Change how the console classifies a command | [`classifyConsoleInput`](../handlers/console.go#L216) in `handlers/console.go` |
| Change what's in a scheduled backup | [`services/backup.go`](../services/backup.go) `CreateBackup`, and the scheduler loop in [`services/backup_scheduler.go`](../services/backup_scheduler.go) |
| Change a log line's level or fields | Every call site uses `log/slog` directly (`slog.Info(...)`, `slog.Error(...)`) — no wrapper to route around |
| Understand why a request 403'd | The response body names the missing permission; check `EffectivePermissions` in [`services/permissions.go`](../services/permissions.go) for that user |
| Understand why a deploy didn't ship | Check CI on `main` first (see [§12](#12-cicd--deployment)) — deploy only runs after CI succeeds |
| Add an env var | Read it with `os.Getenv` where needed, then document it in [§4](#4-configuration-environment-variables) of this file |
| Run the whole thing locally | `docker compose up --build` for the two-container model, or `go run .` against a Minecraft server started some other way (the API degrades gracefully if the control-plane files don't exist yet — see `services/process.go`) |

## 15. Glossary

- **Runtime** — a `*services.ServerRuntime`: one server's directory, log hub, and mutexes. Not a Go runtime, not a container runtime.
- **Registry** — the `servers` SQL table + the in-memory `map[string]*ServerRuntime` built from it at boot.
- **Supervisor** — `cmd/supervisor`, the *other* binary in this repo, running inside the `minecraft` container.
- **Control plane** — the three files (`console.in`, `control.in`, `status.json`) the API and the supervisor use to talk without a network channel.
- **Flat route** — a route without `/servers/:sid/`, always resolving to the default server, kept forever for backward compatibility (selton-mello-bot).
- **Effective permissions** — a role's defaults with one user's overrides layered on top; see [§5.2](#52-permissions--the-rbac-model).
- **Backlog** — the burst of recent log lines a console client receives immediately on connect, from [`readBacklog`](../services/logtail.go#L136).
