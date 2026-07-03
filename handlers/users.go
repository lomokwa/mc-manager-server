package handlers

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/lomokwa/mc-manager/services"
	"github.com/lomokwa/mc-manager/types"
)

func CreateInvitationHandler(c *gin.Context) {
	log.Printf("create invitation request received")

	invitation, err := services.CreateInvitation()
	if err != nil {
		log.Printf("failed to create invitation: %v", err)
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: invitation})
}

func ValidateInvitationHandler(c *gin.Context) {
	token := c.Param("token")

	if err := services.ValidateInvitation(token); err != nil {
		c.JSON(http.StatusNotFound, types.APIResponse{Success: false, Error: "invalid or expired invitation"})
		return
	}

	c.JSON(http.StatusOK, types.APIResponse{Success: true})
}

func RegisterHandler(c *gin.Context) {
	var req types.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: "token, username, and password are required"})
		return
	}

	if err := services.Register(req); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: err.Error()})
		return
	}

	c.JSON(http.StatusCreated, types.APIResponse{Success: true})
}

func GetUsersHandler(c *gin.Context) {
	users, err := services.GetUsers()
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Success: false, Error: "failed to retrieve users"})
		return
	}

	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: users})
}

func LoginHandler(c *gin.Context) {
	var req types.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: "username and password are required"})
		return
	}

	token, err := services.Login(req)
	if err != nil {
		c.JSON(http.StatusUnauthorized, types.APIResponse{Success: false, Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: gin.H{"token": token}})
}
