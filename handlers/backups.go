package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/lomokwa/mc-manager/services"
	"github.com/lomokwa/mc-manager/types"
)

// ListBackupsHandler godoc
// @Summary  List world backups
// @Tags     backups
// @Produce  json
// @Success  200  {object}  types.APIResponse
// @Security ApiKeyAuth
// @Router   /api/backups [get]
func ListBackupsHandler(c *gin.Context) {
	backups, err := services.ListBackups()
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: backups})
}

// CreateBackupHandler godoc
// @Summary  Create a world backup now
// @Tags     backups
// @Produce  json
// @Success  200  {object}  types.APIResponse
// @Security ApiKeyAuth
// @Router   /api/backups [post]
func CreateBackupHandler(c *gin.Context) {
	info, err := services.CreateBackup()
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: info})
}

// DeleteBackupHandler godoc
// @Summary  Delete a backup
// @Tags     backups
// @Produce  json
// @Param    name  query  string  true  "Backup file name"
// @Success  200  {object}  types.APIResponse
// @Security ApiKeyAuth
// @Router   /api/backups [delete]
func DeleteBackupHandler(c *gin.Context) {
	if err := services.DeleteBackup(c.Query("name")); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, types.APIResponse{Success: true})
}

type restoreBackupRequest struct {
	Name string `json:"name"`
}

// RestoreBackupHandler godoc
// @Summary  Restore a backup (server must be stopped)
// @Tags     backups
// @Accept   json
// @Produce  json
// @Param    request  body  handlers.restoreBackupRequest  true  "Backup name"
// @Success  200  {object}  types.APIResponse
// @Security ApiKeyAuth
// @Router   /api/backups/restore [post]
func RestoreBackupHandler(c *gin.Context) {
	var req restoreBackupRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "invalid request body"})
		return
	}
	if err := services.RestoreBackup(req.Name); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, types.APIResponse{Success: true})
}

// GetBackupConfigHandler godoc
// @Summary  Get the periodic-backup config
// @Tags     backups
// @Produce  json
// @Success  200  {object}  types.APIResponse
// @Security ApiKeyAuth
// @Router   /api/backups/config [get]
func GetBackupConfigHandler(c *gin.Context) {
	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: services.LoadBackupConfig()})
}

// UpdateBackupConfigHandler godoc
// @Summary  Update the periodic-backup config
// @Tags     backups
// @Accept   json
// @Produce  json
// @Param    request  body  types.BackupConfig  true  "Backup config"
// @Success  200  {object}  types.APIResponse
// @Security ApiKeyAuth
// @Router   /api/backups/config [put]
func UpdateBackupConfigHandler(c *gin.Context) {
	var cfg types.BackupConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "invalid request body"})
		return
	}
	if err := services.SaveBackupConfig(cfg); err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: services.LoadBackupConfig()})
}
