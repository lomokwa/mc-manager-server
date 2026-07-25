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
	api.POST("/server", handlers.CreateServerHandler)
	api.GET("/server", handlers.ServerExistsHandler)
	api.DELETE("/server", handlers.DeleteServerHandler)
	api.POST("/start", handlers.StartServerHandler)
	api.POST("/stop", handlers.StopServerHandler)
	api.GET("/players", handlers.ListPlayersHandler)
	api.GET("/properties", handlers.GetServerPropertiesHandler)
	api.PATCH("/properties", handlers.UpdateServerPropertiesHandler)
	api.GET("/users", handlers.GetUsersHandler)

	// File manager
	api.GET("/files", handlers.ListFilesHandler)
	api.GET("/files/read", handlers.ReadFileHandler)
	api.PUT("/files", handlers.WriteFileHandler)
	api.GET("/files/download", handlers.DownloadFileHandler)
	api.POST("/files/upload", handlers.UploadFileHandler)
	api.DELETE("/files", handlers.DeleteFileHandler)

	// Backups
	api.GET("/backups", handlers.ListBackupsHandler)
	api.POST("/backups", handlers.CreateBackupHandler)
	api.DELETE("/backups", handlers.DeleteBackupHandler)
	api.GET("/backups/download", handlers.DownloadBackupHandler)
	api.POST("/backups/restore", handlers.RestoreBackupHandler)
	api.GET("/backups/config", handlers.GetBackupConfigHandler)
	api.PUT("/backups/config", handlers.UpdateBackupConfigHandler)

	// Admin Routes (API key)
	admin := r.Group("/api/admin", middleware.ValidateAPIKeyOrJWT())
	admin.POST("/invitations", handlers.CreateInvitationHandler)

	// Public Routes
	r.GET("/api/invitations/:token", handlers.ValidateInvitationHandler)
	r.POST("/api/register", handlers.RegisterHandler)
	r.POST("/api/login", handlers.LoginHandler)

	// Console WebSocket
	api.GET("/console", handlers.ConsoleHandler)

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
