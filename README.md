# MC Manager
MC Manager is a tool for managing Minecraft servers, built with my homelab in mind. It allows you to easily create, manage, and monitor minecraft servers from a web interface.

## Tech Requirements / Stack
- Go 1.25+
- Docker & Docker Compose
- Java 25 (provided by the Docker image)

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

This builds the image from the `Dockerfile`, which:
- Installs Go 1.25 and Java 25
- Downloads Go dependencies
- Generates Swagger docs and compiles the binary
- Exposes ports `8080` (API) and `25565` (Minecraft)

The `minecraft-server/` directory is mounted as a volume so world data persists across container restarts.

### Volumes

| Host path | Container path | Purpose |
|---|---|---|
| `./minecraft-server` | `/app/minecraft-server` | Minecraft world data, configs, JARs |

### Ports

| Port | Service |
|---|---|
| `8080` | Go API server |
| `25565` | Minecraft server |

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
