package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/lomokwa/mc-manager/services"
	"github.com/lomokwa/mc-manager/types"
)

// GetWorldHandler godoc
// @Summary      Get world info
// @Description  Returns the active level name and world spawn coordinates. Spawn is omitted when level.dat can't be read.
// @Tags         world
// @Produce      json
// @Success      200  {object}  types.APIResponse
// @Security     ApiKeyAuth
// @Router       /api/world [get]
func GetWorldHandler(c *gin.Context) {
	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: services.GetWorldInfo()})
}
