package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"net/mail"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/lomokwa/mc-manager/services"
	"github.com/lomokwa/mc-manager/types"
)

func ForgotPasswordHandler(c *gin.Context) {
	var req types.ForgotPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: "email is required"})
		return
	}

	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if _, err := mail.ParseAddress(req.Email); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: "a valid email is required"})
		return
	}

	if err := services.ForgotPassword(req); err != nil {
		slog.Error("forgot password failed", "err", err)
	}

	c.JSON(http.StatusOK, types.APIResponse{
		Success: true,
		Data:    gin.H{"message": "If that account exists, we've sent a password reset email."},
	})
}

func ResetPasswordHandler(c *gin.Context) {
	var req types.ResetPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: "token and password are required"})
		return
	}

	err := services.ResetPassword(req)
	switch {
	case err == nil:
		c.JSON(http.StatusOK, types.APIResponse{Success: true})
	case errors.Is(err, services.ErrInvalidToken):
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: "invalid or expired reset link"})
	default:
		slog.Error("reset password failed", "err", err)
		c.JSON(http.StatusInternalServerError, types.APIResponse{Success: false, Error: "failed to reset password"})
	}
}
