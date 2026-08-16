package main

//go:generate go run github.com/swaggo/swag/cmd/swag@latest init

import (
	"fmt"
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/lomokwa/mc-manager/db"
	"github.com/lomokwa/mc-manager/handlers"
	"github.com/lomokwa/mc-manager/middleware"
	"github.com/lomokwa/mc-manager/services"
	"github.com/lomokwa/mc-manager/types"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	_ "github.com/lomokwa/mc-manager/docs"
)

// pprofAddr is loopback-only: pprof's own handlers (a 30s CPU profile, a full goroutine dump) are
// diagnostic-only and were never meant to sit behind JWT/API-key auth like the rest of the API, so this
// listens on a separate port bound to 127.0.0.1 instead of joining Gin's public router. Reachable with
// `docker exec <container> wget -qO- http://127.0.0.1:6060/debug/pprof/goroutine?debug=2`, never from
// outside the container.
const pprofAddr = "127.0.0.1:6060"

// @title MC Manager API
// @version 1.0
// @description API for managing a Minecraft server
// @host localhost:8080
// @BasePath /
func main() {
	// `./server healthcheck` is what docker-compose's healthcheck runs. It has
	// to come first -- before even the logging setup: it must not boot the DB,
	// the tailer or the scheduler, and must not depend on anything the real
	// boot sequence sets up. See runHealthcheck for why the probe is the
	// binary itself.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(runHealthcheck())
	}

	setupLogging()

	if err := godotenv.Load(); err != nil {
		slog.Info("no .env file found, using system environment")
	}

	go func() {
		slog.Info("pprof diagnostics listening", "addr", pprofAddr, "scope", "container-local only")
		if err := http.ListenAndServe(pprofAddr, nil); err != nil {
			slog.Error("pprof listener failed to start", "err", err)
		}
	}()

	// Initialize database
	db.Init(os.Getenv("DB_PATH"))

	if err := services.EnsureBuiltinRoles(); err != nil {
		fatal("failed to seed built-in roles", err)
	}
	// Assigns roles from ./permissions-seed.json, if present -- see
	// services/seed.go. Runs every boot; a no-op for anyone already assigned.
	services.ApplyPermissionsSeed()
	// Safety net: if the seed file didn't cover anyone (missing, or its
	// listed users haven't registered yet), don't leave every account
	// deny-by-default the moment this deploys -- see EnsureBootstrapOwner.
	services.EnsureBootstrapOwner()

	// Seed the server registry (idempotent no-op after the first boot -- see
	// EnsureDefaultServer) and build the per-server runtime map from it. This
	// is PLAN-multi-server.md Phase 1: a registry that today describes
	// exactly the one server that already existed, at exactly the directory
	// it already lived in -- nothing on disk moves.
	//
	// LoadRuntimes also starts each runtime's log tailer, including the
	// default's, which is what StartLogTailer() used to do directly here.
	// That must happen before any handler can be reached, since GetLogHub()
	// (which now reads DefaultRuntime().Hub) is expected to be non-nil from
	// here on — the JVM itself runs in a separate container (see
	// cmd/supervisor), so this is how the API learns what it's doing.
	if err := services.EnsureDefaultServer(); err != nil {
		fatal("failed to seed default server", err)
	}
	if err := services.LoadRuntimes(); err != nil {
		fatal("failed to load server runtimes", err)
	}

	// Start the automatic backup scheduler
	services.StartBackupScheduler()

	// Default to release mode (quieter, no debug overhead); set GIN_MODE=debug
	// locally to get gin's verbose per-request logging during development.
	if mode := os.Getenv("GIN_MODE"); mode != "" {
		gin.SetMode(mode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	r := newRouter()
	r.Run()
}

// newRouter builds the full route table -- CORS, the rate limiter, auth
// middleware, and every route, flat and namespaced alike. Split out from
// main() so main_routes_test.go can exercise the real router end-to-end
// (proving the flat routes and their /api/servers/:sid equivalents actually
// route, and behave the same way) without also pulling in main()'s
// process-level side effects: godotenv, the pprof listener, the DB/registry
// boot sequence's log.Fatalf calls, or r.Run()'s blocking listener.
func newRouter() *gin.Engine {
	// gin.Default() is gin.New() + Logger() + Recovery(); this is the same
	// thing with the stock logger swapped for one that redacts credentials
	// from the logged URL. Recovery is kept exactly as before.
	r := gin.New()
	r.Use(requestLogger(), gin.Recovery())

	// Cors config
	r.Use(cors.New(cors.Config{
		AllowOrigins:     allowedOrigins(),
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "X-API-Key", "ngrok-skip-browser-warning"},
		AllowCredentials: true,
	}))

	// Rate limiter: 10 requests/sec, burst of 20
	limiter := middleware.NewRateLimiter(10, 20)
	r.Use(limiter.Middleware())

	// JWT Routes
	api := r.Group("/api", middleware.ValidateJWT())
	perm := middleware.RequirePermission // local alias, every route below reads as one line
	api.POST("/server", perm(types.PermServerStart), handlers.CreateServerHandler)
	api.GET("/server", handlers.ServerExistsHandler) // trivial, non-sensitive existence check
	api.DELETE("/server", perm(types.PermServerStop), handlers.DeleteServerHandler)
	api.POST("/start", perm(types.PermServerStart), handlers.StartServerHandler)
	api.POST("/stop", perm(types.PermServerStop), handlers.StopServerHandler)
	api.GET("/players", perm(types.PermPlayersView), handlers.ListPlayersHandler)
	api.GET("/properties", perm(types.PermSettingsView), handlers.GetServerPropertiesHandler)
	api.PATCH("/properties", perm(types.PermSettingsEdit), handlers.UpdateServerPropertiesHandler)
	api.GET("/users", perm(types.PermAdminManageUsers), handlers.GetUsersHandler)

	// File manager
	api.GET("/files", perm(types.PermFilesRead), handlers.ListFilesHandler)
	api.GET("/files/read", perm(types.PermFilesRead), handlers.ReadFileHandler)
	api.PUT("/files", perm(types.PermFilesEdit), handlers.WriteFileHandler)
	api.GET("/files/download", perm(types.PermFilesRead), handlers.DownloadFileHandler)
	api.POST("/files/upload", perm(types.PermFilesUpload), handlers.UploadFileHandler)
	api.DELETE("/files", perm(types.PermFilesDelete), handlers.DeleteFileHandler)

	// Backups
	api.GET("/backups", perm(types.PermBackupsView), handlers.ListBackupsHandler)
	api.POST("/backups", perm(types.PermBackupsCreate), handlers.CreateBackupHandler)
	api.DELETE("/backups", perm(types.PermBackupsDelete), handlers.DeleteBackupHandler)
	api.GET("/backups/download", perm(types.PermBackupsDownload), handlers.DownloadBackupHandler)
	api.POST("/backups/restore", perm(types.PermBackupsRestore), handlers.RestoreBackupHandler)
	api.GET("/backups/config", perm(types.PermBackupsView), handlers.GetBackupConfigHandler)
	api.PUT("/backups/config", perm(types.PermBackupsCreate), handlers.UpdateBackupConfigHandler)

	api.GET("/me", handlers.GetMeHandler)

	// Permissions & roles
	api.GET("/permissions/schema", handlers.PermissionSchemaHandler)
	api.GET("/me/permissions", handlers.MyPermissionsHandler)
	api.GET("/roles", perm(types.PermAdminManageRoles), handlers.ListRolesHandler)
	api.GET("/users/:id/permissions", perm(types.PermAdminManageRoles), handlers.GetUserPermissionsHandler)
	api.PUT("/users/:id/role", perm(types.PermAdminManageRoles), handlers.SetUserRoleHandler)
	api.PUT("/users/:id/overrides", perm(types.PermAdminManageRoles), handlers.SetUserOverridesHandler)

	// Minecraft account linking (self-service, no extra permission beyond login)
	api.GET("/me/mclink", handlers.GetMcLinkHandler)
	api.POST("/me/mclink/start", handlers.StartMcLinkHandler)
	api.POST("/me/mclink/verify", handlers.VerifyMcLinkHandler)
	api.DELETE("/me/mclink", handlers.UnlinkMcHandler)

	// Admin Routes (API key)
	admin := r.Group("/api/admin", middleware.ValidateAPIKeyOrJWT())
	admin.POST("/invitations", handlers.CreateInvitationHandler)

	// Public Routes
	r.GET("/api/invitations/:token", handlers.ValidateInvitationHandler)
	r.POST("/api/register", handlers.RegisterHandler)
	r.POST("/api/login", handlers.LoginHandler)

	// Liveness probe for docker-compose's healthcheck. Unauthenticated on
	// purpose -- a probe that needs a JWT can't run before anyone has logged
	// in -- so it answers exactly one question and reveals nothing else: no
	// version, no server state, no counts. "Is this process listening and
	// routing?" is already observable by anyone who can reach the port.
	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Console WebSocket
	api.GET("/console", perm(types.PermConsoleRead), handlers.ConsoleHandler)

	// Server Health check
	api.GET("/status", handlers.StatusHandler)

	// Server registry (PLAN-multi-server.md D3): list every server, or
	// inspect one, each with its live status folded in -- see
	// handlers/servers.go. No extra permission beyond the JWT ValidateJWT
	// already requires, same gate as GET /api/status just above; see
	// ListServersHandler's own doc comment for why. GetServerHandler needs
	// the same :sid -> runtime resolution (and 404-on-unknown-id) as the
	// namespaced action routes below, so it also runs ResolveServer.
	api.GET("/servers", handlers.ListServersHandler)
	api.GET("/servers/:sid", middleware.ResolveServer(), handlers.GetServerHandler)

	// Namespaced per-server routes (PLAN-multi-server.md D3): the SAME
	// handlers as their flat counterparts above, mounted under
	// /api/servers/:sid with the SAME permission on each one -- only the URL
	// and the runtime a request resolves to differ. ResolveServer 404s an
	// unknown :sid before any handler below runs; a matched :sid stores its
	// *services.ServerRuntime in the context, which each handler reads via
	// runtimeFromRequest (handlers/runtime.go) instead of falling back to
	// the default runtime the way reaching it through the flat route above
	// does.
	//
	// The flat routes above are NOT removed, deprecated, or redirected --
	// per PLAN-multi-server.md D3 they stay forever as aliases for the
	// "default" server. The Discord bot (selton-mello-bot, a separately
	// deployed service) calls /api/players today with no way to learn about
	// /api/servers/:sid/... on its own schedule; breaking the flat routes
	// breaks it in production. See main_routes_test.go's
	// TestFlatRoutes_StillRouteAndMatchDefaultServer.
	serverScoped := api.Group("/servers/:sid", middleware.ResolveServer())
	serverScoped.GET("/status", handlers.StatusHandler)
	serverScoped.POST("/start", perm(types.PermServerStart), handlers.StartServerHandler)
	serverScoped.POST("/stop", perm(types.PermServerStop), handlers.StopServerHandler)
	serverScoped.GET("/console", perm(types.PermConsoleRead), handlers.ConsoleHandler)
	serverScoped.GET("/players", perm(types.PermPlayersView), handlers.ListPlayersHandler)
	serverScoped.GET("/properties", perm(types.PermSettingsView), handlers.GetServerPropertiesHandler)
	serverScoped.PATCH("/properties", perm(types.PermSettingsEdit), handlers.UpdateServerPropertiesHandler)

	serverScoped.GET("/files", perm(types.PermFilesRead), handlers.ListFilesHandler)
	serverScoped.GET("/files/read", perm(types.PermFilesRead), handlers.ReadFileHandler)
	serverScoped.PUT("/files", perm(types.PermFilesEdit), handlers.WriteFileHandler)
	serverScoped.GET("/files/download", perm(types.PermFilesRead), handlers.DownloadFileHandler)
	serverScoped.POST("/files/upload", perm(types.PermFilesUpload), handlers.UploadFileHandler)
	serverScoped.DELETE("/files", perm(types.PermFilesDelete), handlers.DeleteFileHandler)

	serverScoped.GET("/backups", perm(types.PermBackupsView), handlers.ListBackupsHandler)
	serverScoped.POST("/backups", perm(types.PermBackupsCreate), handlers.CreateBackupHandler)
	serverScoped.DELETE("/backups", perm(types.PermBackupsDelete), handlers.DeleteBackupHandler)
	serverScoped.GET("/backups/download", perm(types.PermBackupsDownload), handlers.DownloadBackupHandler)
	serverScoped.POST("/backups/restore", perm(types.PermBackupsRestore), handlers.RestoreBackupHandler)
	serverScoped.GET("/backups/config", perm(types.PermBackupsView), handlers.GetBackupConfigHandler)
	serverScoped.PUT("/backups/config", perm(types.PermBackupsCreate), handlers.UpdateBackupConfigHandler)

	// Serve API Docs
	r.GET("/api/docs/*any", func(c *gin.Context) {
		if c.Param("any") == "/" || c.Param("any") == "" {
			c.Redirect(http.StatusMovedPermanently, "/api/docs/index.html")
			return
		}
		ginSwagger.WrapHandler(swaggerFiles.Handler, ginSwagger.DefaultModelsExpandDepth(-1), ginSwagger.URL("/api/docs/doc.json"))(c)
	})

	return r
}

// runHealthcheck probes the API from inside its own container and returns the
// exit code docker reads: 0 healthy, 1 not. Probing with the binary we already
// ship is what keeps the runtime image free of curl/wget -- nothing extra in
// the image, nothing extra on the attack surface, and the probe can never
// drift from the port the server actually binds.
//
// It talks to 127.0.0.1, which also keeps it clear of the rate limiter: that
// buckets per c.ClientIP(), so probe traffic and real traffic never share a
// bucket and a burst of external requests can't starve the healthcheck into
// failing a container that is in fact fine.
func runHealthcheck() int {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + listenPort() + "/healthz")
	if err != nil {
		fmt.Fprintf(os.Stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "healthcheck: HTTP %d\n", resp.StatusCode)
		return 1
	}
	return 0
}

// listenPort mirrors how gin's r.Run() picks a port (the PORT env var, else
// 8080) so the probe always aims at wherever the server really listens.
func listenPort() string {
	if p := strings.TrimSpace(os.Getenv("PORT")); p != "" {
		return p
	}
	return "8080"
}

// allowedOrigins returns the CORS allow-list. It reads a comma-separated
// CORS_ALLOWED_ORIGINS env var and falls back to the local dev origins
// (the Vite dev server and the API host) when it is unset.
func allowedOrigins() []string {
	raw := os.Getenv("CORS_ALLOWED_ORIGINS")
	if strings.TrimSpace(raw) == "" {
		return []string{"http://localhost:5173", "http://localhost:8080"}
	}

	parts := strings.Split(raw, ",")
	origins := make([]string, 0, len(parts))
	for _, p := range parts {
		if o := strings.TrimSpace(p); o != "" {
			origins = append(origins, o)
		}
	}
	return origins
}

// credentialParamRe matches the `key` and `token` query parameters so their
// values can be stripped from access logs. Both genuinely travel in the URL:
// the API key may be passed as ?key= , and the console WebSocket has to send
// its JWT as ?token= because browsers can't set headers on a WS handshake.
// Without this they are written verbatim into every log line and survive in
// whatever aggregates those logs.
var credentialParamRe = regexp.MustCompile(`([?&](?:key|token)=)[^&]*`)

// requestLogger is gin's access logger with credentials redacted from the
// logged URL. gin puts the raw query string in LogFormatterParams.Path, which
// is exactly where those values would otherwise appear.
func requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		// Captured before c.Next(): a handler is free to rewrite the URL, and
		// the line should describe what was asked for, not what it became.
		uri := c.Request.URL.RequestURI()

		c.Next()

		status := c.Writer.Status()

		// The status picks the level, which is the entire point of the
		// exercise: `| json | level="ERROR"` in Loki now means "the API is
		// failing", and 4xx surfacing as WARN is what makes a wave of denials
		// visible instead of silent. That is not hypothetical -- a service
		// account left without a role produced exactly that wave today, and
		// nothing in the logs distinguished it from ordinary traffic.
		level := slog.LevelInfo
		switch {
		case status >= 500:
			level = slog.LevelError
		case status >= 400:
			level = slog.LevelWarn
		}

		slog.Log(c.Request.Context(), level, "http request",
			"status", status,
			"method", c.Request.Method,
			// Redaction happens here, on the way out, so there is exactly one
			// place a credential could leak from -- see main_redaction_test.go.
			"path", credentialParamRe.ReplaceAllString(uri, "${1}REDACTED"),
			"latency_ms", time.Since(start).Milliseconds(),
			"ip", c.ClientIP(),
		)
	}
}
