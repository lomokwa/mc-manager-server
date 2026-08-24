package handlers

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/lomokwa/mc-manager/middleware"
	"github.com/lomokwa/mc-manager/services"
	"github.com/lomokwa/mc-manager/types"
)

func CreateInvitationHandler(c *gin.Context) {
	slog.Debug("create invitation request received")

	invitation, err := services.CreateInvitation()
	if err != nil {
		slog.Error("failed to create invitation", "err", err)
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

// GetMeHandler returns the caller's own account info -- self-service, no
// admin.manage_users needed, unlike the full GetUsersHandler list.
func GetMeHandler(c *gin.Context) {
	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, types.APIResponse{Error: "missing or invalid session"})
		return
	}
	user, err := services.GetUserByID(userID)
	if err != nil {
		c.JSON(http.StatusNotFound, types.APIResponse{Error: "user not found"})
		return
	}
	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: user})
}

// UpdateProfileHandler lets the caller edit their own profile -- self-service,
// no admin.manage_users permission needed, same pattern as GetMeHandler and
// the mclink endpoints below.
func UpdateProfileHandler(c *gin.Context) {
	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, types.APIResponse{Error: "missing or invalid session"})
		return
	}

	var req types.UpdateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: "invalid request body"})
		return
	}

	if err := types.ValidateDisplayName(req.DisplayName); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: err.Error()})
		return
	}

	if err := services.UpdateDisplayName(userID, req.DisplayName); err != nil {
		slog.Error("failed to update display name", "err", err)
		c.JSON(http.StatusInternalServerError, types.APIResponse{Success: false, Error: "failed to update profile"})
		return
	}

	user, err := services.GetUserByID(userID)
	if err != nil {
		c.JSON(http.StatusNotFound, types.APIResponse{Error: "user not found"})
		return
	}
	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: user})
}

// UpdateEmailHandler lets the caller set or clear their own email address --
// self-service, same pattern as UpdateProfileHandler, but a separate
// endpoint so saving it never touches display_name (and vice versa).
func UpdateEmailHandler(c *gin.Context) {
	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, types.APIResponse{Error: "missing or invalid session"})
		return
	}

	var req types.UpdateEmailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: "invalid request body"})
		return
	}

	if err := types.ValidateEmail(req.Email); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: err.Error()})
		return
	}

	if err := services.UpdateEmail(userID, req.Email); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: err.Error()})
		return
	}

	user, err := services.GetUserByID(userID)
	if err != nil {
		c.JSON(http.StatusNotFound, types.APIResponse{Error: "user not found"})
		return
	}
	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: user})
}

// ChangePasswordHandler lets the caller rotate their own password after
// proving they know the current one -- self-service, same auth pattern as
// GetMeHandler.
func ChangePasswordHandler(c *gin.Context) {
	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, types.APIResponse{Error: "missing or invalid session"})
		return
	}

	var req types.ChangePasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: "current_password and new_password are required"})
		return
	}

	if err := types.ValidatePassword(req.NewPassword); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: err.Error()})
		return
	}

	if err := services.ChangePassword(userID, req.CurrentPassword, req.NewPassword); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: err.Error()})
		return
	}

	c.JSON(http.StatusOK, types.APIResponse{Success: true})
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
