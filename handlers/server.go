package handlers

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/lomokwa/mc-manager/services"
	"github.com/lomokwa/mc-manager/types"
	"github.com/lomokwa/mc-manager/utils"
)

// @Summary Create a new Minecraft server
// @Description Downloads the server jar and prepares server files
// @Tags server
// @Accept json
// @Produce json
// @Param request body types.CreateServerRequest true "Server configuration"
// @Success 201 {object} types.APIResponse
// @Failure 400 {object} types.APIResponse
// @Failure 500 {object} types.APIResponse
// @Security BearerAuth
// @Router /api/server [post]
func CreateServerHandler(c *gin.Context) {
	log.Printf("create server request received")

	var req types.CreateServerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "invalid request body"})
		return
	}

	if err := types.ValidateServerProperties(req.Properties); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: err.Error()})
		return
	}

	if utils.FileExists(services.ServerJarPath) {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "server already exists"})
		return
	}

	switch req.ServerType {
	case "vanilla":
		if err := services.DownloadServerJar(services.ServerJarPath, req.ReleaseVersion); err != nil {
			log.Printf("failed to download server.jar: %v", err)
			c.JSON(http.StatusInternalServerError, types.APIResponse{Error: err.Error()})
			return
		}
	case "fabric":
		if err := services.DownloadFabricJar(services.ServerJarPath, req.ReleaseVersion, req.LoaderVersion); err != nil {
			log.Printf("failed to download server.jar: %v", err)
			c.JSON(http.StatusInternalServerError, types.APIResponse{Error: err.Error()})
			return
		}
	default:
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "unsupported server type"})
		return
	}

	log.Printf("server.jar downloaded successfully")

	log.Printf("creating server files")
	if err := services.PrepareServerFiles(services.ServerDir, req.CreateLaunchScript, req.ConfigureProperties, req.Properties); err != nil {
		log.Printf("failed to prepare server files: %v", err)
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: err.Error()})
		return
	}

	meta := services.ServerMeta{
		ServerType:    req.ServerType,
		GameVersion:   req.ReleaseVersion,
		LoaderVersion: req.LoaderVersion,
	}
	if err := services.SaveServerMeta(meta); err != nil {
		log.Printf("failed to save server meta: %v", err)
	}

	c.JSON(http.StatusCreated, types.APIResponse{Success: true})
}

// @Summary Start the Minecraft server
// @Description Starts the Minecraft server process
// @Tags server
// @Produce json
// @Success 200 {object} types.APIResponse
// @Failure 400 {object} types.APIResponse
// @Security BearerAuth
// @Router /api/start [post]
func StartServerHandler(c *gin.Context) {
	log.Printf("start request received")

	if !utils.FileExists(services.ServerJarPath) {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "server not created yet, use POST /api/server first"})
		return
	}

	output, err := services.StartServerProcess()
	if err != nil {
		log.Printf("failed to start server process: %v", err)
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: err.Error()})
		return
	}

	log.Printf("server process started")
	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: output})
}

// @Summary Delete the Minecraft server
// @Description Removes the server jar and all server files
// @Tags server
// @Produce json
// @Success 200 {object} types.APIResponse
// @Failure 400 {object} types.APIResponse
// @Failure 500 {object} types.APIResponse
// @Security BearerAuth
// @Router /api/server [delete]
func DeleteServerHandler(c *gin.Context) {
	log.Printf("delete server request received")

	if services.IsServerRunning() {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "cannot delete server while it is running"})
		return
	}

	if !utils.FileExists(services.ServerJarPath) {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "no server to delete"})
		return
	}

	if err := services.DeleteServer(); err != nil {
		log.Printf("failed to delete server: %v", err)
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: err.Error()})
		return
	}

	log.Printf("server deleted successfully")
	c.JSON(http.StatusOK, types.APIResponse{Success: true})
}

// @Summary Check if server exists
// @Description Returns whether a server jar has been created
// @Tags server
// @Produce json
// @Success 200 {object} types.APIResponse
// @Security BearerAuth
// @Router /api/server [get]
func ServerExistsHandler(c *gin.Context) {
	exists := utils.FileExists(services.ServerJarPath)
	data := gin.H{"exists": exists}

	if exists {
		if meta, err := services.LoadServerMeta(); err == nil {
			data["serverType"] = meta.ServerType
			data["gameVersion"] = meta.GameVersion
			data["loaderVersion"] = meta.LoaderVersion
		}
	}

	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: data})
}

// @Summary Stop the Minecraft server
// @Description Stops the running Minecraft server process
// @Tags server
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} map[string]interface{}
// @Router /api/stop [post]
func StopServerHandler(c *gin.Context) {
	log.Printf("stop request received")

	output, err := services.StopServerProcess()
	if err != nil {
		log.Printf("failed to stop server process: %v", err)
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: err.Error()})
		return
	}

	log.Printf("server process stopped")
	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: output})
}

// @Summary Get server status
// @Description Returns whether the Minecraft server is currently running
// @Tags server
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/status [get]
func StatusHandler(c *gin.Context) {
	log.Printf("status request received")
	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: gin.H{"running": services.IsServerRunning()}})
}

// @Summary Get recent server logs
// @Description Returns the most recent buffered console output (oldest line first, up to the replay buffer size). Empty when the server is not running. For a live stream use the /api/console WebSocket.
// @Tags server
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/logs [get]
func LogsHandler(c *gin.Context) {
	log.Printf("logs request received")

	lines := []string{}
	if hub := services.GetLogHub(); hub != nil {
		lines = hub.Snapshot()
	}

	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: gin.H{"lines": lines}})
}
