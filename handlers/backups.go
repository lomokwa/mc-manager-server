package handlers

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/lomokwa/mc-manager/services"
	"github.com/lomokwa/mc-manager/types"
)

// @Summary List backups
// @Description Lists all backup archives, newest first
// @Tags backups
// @Produce json
// @Success 200 {object} types.APIResponse
// @Failure 500 {object} types.APIResponse
// @Security BearerAuth
// @Router /api/backups [get]
func ListBackupsHandler(c *gin.Context) {
	backups, err := services.ListBackups()
	if err != nil {
		log.Printf("failed to list backups: %v", err)
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: "failed to list backups"})
		return
	}

	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: backups})
}

// @Summary Create a backup
// @Description Creates a new backup of the world directory
// @Tags backups
// @Produce json
// @Success 201 {object} types.APIResponse
// @Failure 400 {object} types.APIResponse
// @Failure 500 {object} types.APIResponse
// @Security BearerAuth
// @Router /api/backups [post]
func CreateBackupHandler(c *gin.Context) {
	log.Printf("create backup request received")

	info, err := services.CreateBackup()
	if err != nil {
		log.Printf("failed to create backup: %v", err)
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: err.Error()})
		return
	}

	log.Printf("backup created: %s", info.Name)
	c.JSON(http.StatusCreated, types.APIResponse{Success: true, Data: info})
}

// @Summary Delete a backup
// @Description Deletes a single backup archive by name
// @Tags backups
// @Produce json
// @Param name query string true "Backup name"
// @Success 200 {object} types.APIResponse
// @Failure 400 {object} types.APIResponse
// @Security BearerAuth
// @Router /api/backups [delete]
func DeleteBackupHandler(c *gin.Context) {
	name := c.Query("name")
	if name == "" {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "name is required"})
		return
	}

	if err := services.DeleteBackup(name); err != nil {
		log.Printf("failed to delete backup %q: %v", name, err)
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: err.Error()})
		return
	}

	log.Printf("backup deleted: %s", name)
	c.JSON(http.StatusOK, types.APIResponse{Success: true})
}

type restoreBackupRequest struct {
	Name string `json:"name" binding:"required"`
}

// @Summary Restore a backup
// @Description Restores the world directory from a backup archive. The server must be stopped first.
// @Tags backups
// @Accept json
// @Produce json
// @Param request body restoreBackupRequest true "Backup to restore"
// @Success 200 {object} types.APIResponse
// @Failure 400 {object} types.APIResponse
// @Security BearerAuth
// @Router /api/backups/restore [post]
func RestoreBackupHandler(c *gin.Context) {
	var req restoreBackupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "invalid request body"})
		return
	}

	log.Printf("restore backup request received: %s", req.Name)

	if err := services.RestoreBackup(req.Name); err != nil {
		log.Printf("failed to restore backup %q: %v", req.Name, err)
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: err.Error()})
		return
	}

	log.Printf("backup restored: %s", req.Name)
	c.JSON(http.StatusOK, types.APIResponse{Success: true})
}

// @Summary Get the backup schedule
// @Description Returns the current automatic backup configuration
// @Tags backups
// @Produce json
// @Success 200 {object} types.APIResponse
// @Failure 500 {object} types.APIResponse
// @Security BearerAuth
// @Router /api/backups/config [get]
func GetBackupConfigHandler(c *gin.Context) {
	cfg, err := services.LoadBackupConfig()
	if err != nil {
		log.Printf("failed to load backup config: %v", err)
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: "failed to load backup config"})
		return
	}

	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: cfg})
}

// @Summary Update the backup schedule
// @Description Updates the automatic backup configuration and reloads the scheduler
// @Tags backups
// @Accept json
// @Produce json
// @Param request body types.BackupConfig true "Backup schedule"
// @Success 200 {object} types.APIResponse
// @Failure 400 {object} types.APIResponse
// @Failure 500 {object} types.APIResponse
// @Security BearerAuth
// @Router /api/backups/config [put]
func UpdateBackupConfigHandler(c *gin.Context) {
	var cfg types.BackupConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "invalid request body"})
		return
	}

	if err := types.ValidateBackupConfig(cfg); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: err.Error()})
		return
	}

	if err := services.SaveBackupConfig(cfg); err != nil {
		log.Printf("failed to save backup config: %v", err)
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: "failed to save backup config"})
		return
	}

	services.NotifyBackupConfigChanged()

	log.Printf("backup config updated: enabled=%v interval=%dmin keep=%d", cfg.Enabled, cfg.IntervalMinutes, cfg.Keep)
	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: cfg})
}
