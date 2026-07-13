# Backend Implementation Plan: Server Backups

The frontend (`mc-manager-client/src/pages/backups/Backups.tsx`) already ships a full
Backups page. It calls a `/api/backups*` surface that does not exist on the backend
yet — today the page falls into its `unsupported` state (see `apiFetch` in
`mc-manager-client/src/lib/api.ts`, which treats any 404 as "this server build
doesn't support backups"). This document specifies the backend work needed to make
that page fully functional, derived directly from the frontend's expectations.

## 1. Contract derived from the frontend

All responses must use the existing envelope (`types.APIResponse`):
`{ "success": bool, "data"?: any, "error"?: string }`. A 404 means "unsupported" to
the client, so routes must always be registered (never omitted) once shipped.

| Method | Path                | Request body                                | Response `data`         | Used by                     |
|--------|---------------------|----------------------------------------------|--------------------------|-----------------------------|
| GET    | `/api/backups`        | –                                            | `BackupInfo[]`            | initial list load           |
| POST   | `/api/backups`        | – (204/empty ok)                             | `BackupInfo`               | "Back up now"               |
| DELETE | `/api/backups?name=`  | – (`name` query param)                       | – (`success:true`)        | delete row action            |
| POST   | `/api/backups/restore`| `{ "name": string }`                          | – (`success:true`)        | restore row action           |
| GET    | `/api/backups/config` | –                                            | `BackupConfig`             | schedule panel init           |
| PUT    | `/api/backups/config` | `BackupConfig`                                | `BackupConfig` (saved)     | "Save" schedule button        |

Types (must match frontend field names/casing exactly — frontend uses snake_case
for `BackupConfig`, so use Go struct tags accordingly):

```go
// types/backup.go
type BackupInfo struct {
    Name    string    `json:"name"`
    Size    int64     `json:"size"`
    Created time.Time `json:"created"`
}

type BackupConfig struct {
    Enabled          bool `json:"enabled"`
    IntervalMinutes  int  `json:"interval_minutes"`
    Keep             int  `json:"keep"`
}
```

Frontend behaviors backend must respect:
- Restore button is disabled client-side while the server is running, but the
  **backend must also reject** `POST /api/backups/restore` while running (defense
  in depth) — return 400 with a clear error, matching the `running` guard pattern
  already used in `DeleteServerHandler`.
- `keep: 0` is a valid config value (frontend clamps to `Math.max(0, ...)`); treat
  `0` as "keep unlimited backups" or "keep none after each run" — pick one and
  document it (recommend: `0` = unlimited, since that matches least-surprise for a
  numeric input defaulting from empty state).
- Errors should return a `message`/`error` string; frontend surfaces `r.message` in
  toasts, so avoid generic messages like "internal error".

## 2. Storage layout

- Store backups under `minecraft-server/../backups` (sibling to `ServerDir`, NOT
  inside it, so backup files aren't themselves swept into future backups or wiped
  by `DeleteServer`). Add a constant `services.BackupsDir = "./backups"`.
- Each backup is a single compressed archive of the world directory (and
  optionally `server.properties`, `ops.json`, `whitelist.json` for full restore
  fidelity) — recommend `.zip` via `archive/zip` (stdlib, no new dependency).
- Naming convention: `world-2026-07-13T15-04-05Z.zip` (RFC3339 with `:` replaced by
  `-` for filesystem safety). This is the `name` field returned to the frontend and
  used as the `name` param for restore/delete — treat it as an opaque, sanitized
  identifier (validate with `filepath.Base` + regex to prevent path traversal, same
  pattern as `handlers/files.go`'s `safePath`).

## 3. New files to create

### `types/backup.go`
`BackupInfo`, `BackupConfig` structs as above, plus a `ValidateBackupConfig` func
(reject negative `interval_minutes`/`keep`, cap `interval_minutes` at some sane
minimum e.g. >= 1 to avoid a runaway ticker).

### `services/backup.go`
Core logic, mirroring the style of `services/minecraft.go` / `services/process.go`:
- `ListBackups() ([]types.BackupInfo, error)` — reads `BackupsDir`, stats each
  `.zip`, sorts newest-first (frontend doesn't sort itself).
- `CreateBackup() (types.BackupInfo, error)`:
  - Ensure `BackupsDir` exists (`os.MkdirAll`).
  - If the server is running, send `save-off` then `save-all flush` via
    `services.SendCommand` before zipping, and `save-on` after, to avoid a corrupt
    mid-write snapshot (Minecraft still writes region files during autosave).
    Wrap in a helper so `CreateBackup` behaves correctly whether or not the
    server is running (skip save-off/on entirely when stopped).
  - Zip `world/` (+ config files) into a temp file, then atomically rename into
    `BackupsDir` on success — avoids leaving a partial `.zip` visible to
    `ListBackups` if the process is interrupted.
  - Guard against concurrent backup runs with a `sync.Mutex` (a scheduled backup
    firing while a manual one is in progress must not race).
- `RestoreBackup(name string) error`:
  - Reject if `services.IsServerRunning()` (matches frontend expectation).
  - Validate `name` against path traversal + confirm the file exists in
    `BackupsDir`.
  - Unzip over the current `world/` — safest approach: move existing `world/` to
    `world.bak-<ts>` first, extract, and only delete the `.bak` after a
    successful extract (so a failed restore doesn't destroy the live world).
- `DeleteBackup(name string) error` — validate name, `os.Remove`.
- `PruneBackups(keep int) error` — if `keep > 0`, delete oldest backups beyond
  `keep` count; call this after every successful `CreateBackup` (manual and
  scheduled) if a config with `keep > 0` is active.
- Config persistence: `LoadBackupConfig() (types.BackupConfig, error)` /
  `SaveBackupConfig(cfg types.BackupConfig) error`. Persist in SQLite (new table,
  see §4) rather than a JSON file, consistent with how `users`/`invitations` are
  stored in `db/migrations.sql`. Default config when no row exists:
  `{enabled: false, interval_minutes: 1440, keep: 7}`.

### `services/backup_scheduler.go` (or a section of `backup.go`)
- A single background goroutine (started once from `main.go`, similar to how the
  server process owns a goroutine) that:
  - On startup, loads config; if `enabled`, starts a `time.Ticker` at
    `interval_minutes`.
  - On `PUT /api/backups/config`, the handler must signal the scheduler to
    reload (e.g. a buffered channel `reloadCh chan struct{}`) so a change takes
    effect immediately without restarting the process.
  - Ticker fires `CreateBackup()` + `PruneBackups(cfg.Keep)`; log failures but
    don't crash the ticker loop.
  - Skip a scheduled tick if a backup is already in progress (reuse the mutex
    from `CreateBackup`).

### `handlers/backups.go`
Following the exact conventions in `handlers/server.go` / `handlers/files.go`
(swagger comments, `log.Printf` on entry, `types.APIResponse` on every branch):
- `ListBackupsHandler` — `GET`
- `CreateBackupHandler` — `POST`
- `DeleteBackupHandler` — `DELETE`, reads `name` from `c.Query("name")`
- `RestoreBackupHandler` — `POST`, binds `{name string}` JSON body
- `GetBackupConfigHandler` — `GET`
- `UpdateBackupConfigHandler` — `PUT`, binds `types.BackupConfig`, validates,
  saves, signals scheduler reload, returns saved config as `data` (frontend does
  `if (r.data) setConfig(r.data)`)

## 4. Database migration

Add to `db/migrations.sql` (append-only, `IF NOT EXISTS`, consistent with existing
table style):

```sql
CREATE TABLE IF NOT EXISTS backup_config (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  enabled BOOLEAN NOT NULL DEFAULT 0,
  interval_minutes INTEGER NOT NULL DEFAULT 1440,
  keep INTEGER NOT NULL DEFAULT 7
);
```

Single-row table (`id=1` constraint) — simplest option given there's one server
per backend instance, matching how `services.ServerMeta` is a singleton concept.

## 5. Route registration (`main.go`)

Add under the existing JWT-protected `api` group, near the file manager routes:

```go
// Backups
api.GET("/backups", handlers.ListBackupsHandler)
api.POST("/backups", handlers.CreateBackupHandler)
api.DELETE("/backups", handlers.DeleteBackupHandler)
api.POST("/backups/restore", handlers.RestoreBackupHandler)
api.GET("/backups/config", handlers.GetBackupConfigHandler)
api.PUT("/backups/config", handlers.UpdateBackupConfigHandler)
```

Start the scheduler goroutine in `main()` after `db.Init(...)`, e.g.
`services.StartBackupScheduler()`.

## 6. Swagger

Add `@Summary`/`@Router` annotations per handler (matching existing style) and
regenerate docs via the existing `go generate` directive at the top of
`main.go` (`swag init`).

## 7. Task breakdown (suggested order)

1. `types/backup.go` — structs + validation.
2. DB migration for `backup_config`.
3. `services/backup.go` — `ListBackups`, `CreateBackup`, `DeleteBackup`,
   `RestoreBackup`, `PruneBackups`, config load/save. Unit test zip/unzip and
   prune logic with a temp dir.
4. `handlers/backups.go` — wire handlers to services, with the running-state
   guard on restore.
5. Register routes in `main.go`.
6. `services/backup_scheduler.go` — ticker + reload channel, started from
   `main.go`.
7. Regenerate swagger docs.
8. Manual end-to-end test against the existing frontend page (create, list,
   restore-while-stopped, restore-while-running rejection, delete, schedule
   save/reload, `keep` pruning).

## 8. Edge cases / risks to cover in tests

- No server created yet (`world/` missing) → `CreateBackup` should return a clear
  400, not a panic on zip-walk.
- Restore when no backup with that `name` exists → 404-style `APIResponse` error
  (not an HTTP 404, since that's reserved as the "unsupported" signal to the
  frontend).
- Concurrent manual + scheduled backup → serialized by mutex, second caller
  either waits or gets a "backup already in progress" error (decide and
  document which).
- Disk full during zip write → surface a real error, ensure the partial temp
  file is cleaned up (`defer os.Remove(tmpPath)` before the atomic rename).
- Large worlds → stream files into the zip writer rather than reading whole
  files into memory (`io.Copy`, as already done in `handlers/files.go`'s
  upload handler).
