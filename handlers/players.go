package handlers

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/lomokwa/mc-manager/services"
	"github.com/lomokwa/mc-manager/types"
)

// @Summary List players
// @Description Returns known players from the server's usercache, each annotated with op / banned / whitelisted / online status
// @Tags server
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Failure 500 {object} map[string]interface{}
// @Router /api/players [get]
func ListPlayersHandler(c *gin.Context) {
	log.Printf("list players request received")
	players, err := services.ListPlayers()
	if err != nil {
		log.Printf("failed to list players: %v", err)
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: players})
}
