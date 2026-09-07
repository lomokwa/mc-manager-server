package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/lomokwa/mc-manager/services"
	"github.com/lomokwa/mc-manager/types"
)

// ListActivityHandler godoc
// @Summary      Read the audit trail
// @Description  Newest first. Optional ?category= narrows to one kind of action, ?before= pages back from an id, ?limit= caps the page (default 50, max 200).
// @Tags         activity
// @Produce      json
// @Success      200  {object}  types.APIResponse
// @Security     ApiKeyAuth
// @Router       /api/activity [get]
func ListActivityHandler(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	before, _ := strconv.Atoi(c.Query("before"))

	// An unknown category would otherwise return an empty page that reads like
	// "nothing happened", which is a different claim from "that isn't a thing".
	category := c.Query("category")
	if category != "" && !isKnownCategory(category) {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "unknown category: " + category})
		return
	}

	entries, err := services.ListActivity(category, limit, before)
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: entries})
}

func isKnownCategory(category string) bool {
	for _, c := range types.ActivityCategories {
		if c == category {
			return true
		}
	}
	return false
}
