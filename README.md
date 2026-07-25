# MC Manager
MC Manager is a tool for managing Minecraft servers, built with my homelab in mind. It allows you to easily create, manage, and monitor minecraft servers from a web interface.

## Tech Requirements / Stack
- Go 1.25+
- Docker & Docker Compose
- Java 25 (provided by the `minecraft` service's Docker image)

## Installation

1. Clone the repository:
   ```bash
   git clone https://github.com/lomokwa/mc-manager.git
   cd mc-manager/mc-manager-server
   ```

2. Copy the example environment file and fill in values:
   ```bash
   cp .env.example .env
   ```

   At minimum, set `API_KEY`, `JWT_SECRET`, and `DB_PATH`.

3. Start the application using Docker Compose:
   ```bash
   docker compose up --build
   ```

4. Access the API at `http://localhost:8080` and the frontend at `http://localhost:5173`.

## Docker

### Production

```bash
docker compose up --build -d
```

This builds two separate images and starts both as their own containers:
- `mc-manager` (`Dockerfile`) — downloads Go dependencies, generates Swagger
  docs, and compiles the API binary. No JDK; it only ever talks to the
  Minecraft process through the shared `minecraft-server/` volume.
- `minecraft` (`Dockerfile.minecraft`) — runs the JVM under a small Go
  supervisor (`cmd/supervisor`), so restarting/redeploying the API no longer
  takes the live game server down with it.

The `minecraft-server/` directory is bind-mounted into both containers so world data persists across restarts and redeploys of either one independently.

### Volumes

| Host path | Container path | Service | Purpose |
|---|---|---|---|
| `./minecraft-server` | `/app/minecraft-server` | `mc-manager` | Minecraft world data, configs, JARs |
| `./minecraft-server` | `/mc` | `minecraft` | Same directory, from the JVM's side |
| `./data` | `/app/data` | `mc-manager` | SQLite database |
| `./backups` | `/app/backups` | `mc-manager` | World backup archives |

### Ports

| Port | Service |
|---|---|
| `8080` | Go API server |
| `25565` | Minecraft server |
| `24454/udp` | Simple Voice Chat |

### Development (hot-reload)

```bash
docker compose -f docker-compose.yml -f docker-compose.dev.yml up --build
```

Builds the `mc-manager` image's `dev` target instead (full Go toolchain +
[air](https://github.com/air-verse/air)), mounts the whole project directory
in, and runs `air` so any Go code change triggers an automatic rebuild. The
`minecraft` service is unaffected either way.

### Environment Variables

See [`.env.example`](.env.example) for all available options.

| Variable | Required | Description |
|---|---|---|
| `API_KEY` | Yes | Secret for admin endpoints (`/api/admin/*`) |
| `JWT_SECRET` | Yes | Secret for signing JWT tokens |
| `DB_PATH` | Yes | Path to SQLite database file |
| `CLIENT_URL` | No | Frontend URL for invitation links (default: `http://localhost:5173`) |
| `CORS_ALLOWED_ORIGINS` | No | Comma-separated allowed origins |
| `PORT` | No | API listen port (default: `8080`) |

## Authentication

MC Manager uses **invitation-based registration** with JWT authentication. See [INVITATION_AUTH.md](INVITATION_AUTH.md) for the full registration flow.

**Quick summary:**
1. Admin creates an invitation → gets a link
2. User opens the link → registers with username + password
3. User logs in → receives a JWT
4. JWT is sent on all API requests via `Authorization: Bearer <token>`

## Current Tasks
- [x] Implement server start functionality
- [x] Add server stop functionality
- [x] Add user authentication and authorization
- [ ] Add file upload/download capabilities
- [ ] Add server configuration management
- [ ] Add file management features
- [ ] Implement server logs viewing
- [ ] Add server status monitoring
- [ ] Implement server monitoring features
- [ ] Implement server backup functionality
- [ ] Add a minimal "lobby" server that users are redirected to when the server gets shutdown / restarted
 
## API Docs
API documentation is served via Swagger UI at `http://localhost:8080/api/docs/index.html`.

Docs are generated from comment annotations on handlers using [swaggo](https://github.com/swaggo/swag). To regenerate after editing annotations:
```bash
swag init
```
