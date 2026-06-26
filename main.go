package main

//go:generate go run github.com/swaggo/swag/cmd/swag@latest init

import (
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"github.com/lomokwa/mc-manager/db"
	"github.com/lomokwa/mc-manager/handlers"
	"github.com/lomokwa/mc-manager/middleware"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	_ "github.com/lomokwa/mc-manager/docs"
)

// @title MC Manager API
// @version 1.0
// @description API for managing a Minecraft server
// @host localhost:8080
// @BasePath /
func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file found, using system environment")
	}

	// Initialize database
	db.Init(os.Getenv("DB_PATH"))

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

	// Recent buffered logs (REST snapshot; live stream is /api/console)
	api.GET("/logs", handlers.LogsHandler)

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
