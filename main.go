package main

//go:generate go run github.com/swaggo/swag/cmd/swag@latest init

import (
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"strings"

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
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file found, using system environment")
	}

	go func() {
		log.Printf("pprof diagnostics listening on %s (container-local only)", pprofAddr)
		if err := http.ListenAndServe(pprofAddr, nil); err != nil {
			log.Printf("pprof listener failed to start: %v", err)
		}
	}()

	// Initialize database
	db.Init(os.Getenv("DB_PATH"))

	if err := services.EnsureBuiltinRoles(); err != nil {
		log.Fatalf("failed to seed built-in roles: %v", err)
	}
	// Assigns roles from ./permissions-seed.json, if present -- see
	// services/seed.go. Runs every boot; a no-op for anyone already assigned.
	services.ApplyPermissionsSeed()
	// Safety net: if the seed file didn't cover anyone (missing, or its
	// listed users haven't registered yet), don't leave every account
	// deny-by-default the moment this deploys -- see EnsureBootstrapOwner.
	services.EnsureBootstrapOwner()

	// Start following the Minecraft server's own log file. This must happen
	// before any handler can be reached, since GetLogHub() is expected to be
	// non-nil from here on — the JVM itself now runs in a separate container
	// (see cmd/supervisor), so this is how the API learns what it's doing.
	services.StartLogTailer()

	// Start the automatic backup scheduler
	services.StartBackupScheduler()

	// Default to release mode (quieter, no debug overhead); set GIN_MODE=debug
	// locally to get gin's verbose per-request logging during development.
	if mode := os.Getenv("GIN_MODE"); mode != "" {
		gin.SetMode(mode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	r := gin.Default()

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

	// Console WebSocket
	api.GET("/console", perm(types.PermConsoleRead), handlers.ConsoleHandler)

	// Server Health check
	api.GET("/status", handlers.StatusHandler)

	// Serve API Docs
	r.GET("/api/docs/*any", func(c *gin.Context) {
		if c.Param("any") == "/" || c.Param("any") == "" {
			c.Redirect(http.StatusMovedPermanently, "/api/docs/index.html")
			return
		}
		ginSwagger.WrapHandler(swaggerFiles.Handler, ginSwagger.DefaultModelsExpandDepth(-1), ginSwagger.URL("/api/docs/doc.json"))(c)
	})

	r.Run()
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
