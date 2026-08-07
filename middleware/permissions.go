package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/lomokwa/mc-manager/services"
	"github.com/lomokwa/mc-manager/types"
)

// UserIDFromContext extracts the JWT-authenticated caller's user ID, set by
// ValidateJWT as claims["user_id"] -- a JSON number, so it decodes to
// float64, never int, at this layer.
func UserIDFromContext(c *gin.Context) (int, bool) {
	raw, exists := c.Get("userID")
	if !exists {
		return 0, false
	}
	id, ok := raw.(float64)
	if !ok {
		return 0, false
	}
	return int(id), true
}

// RequirePermission 403s unless the authenticated caller holds perm. Must run
// after ValidateJWT, which is what populates the context this reads from.
func RequirePermission(perm types.Permission) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := UserIDFromContext(c)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, types.APIResponse{Error: "missing or invalid session"})
			return
		}
		if !services.HasPermission(userID, perm) {
			c.AbortWithStatusJSON(http.StatusForbidden, types.APIResponse{
				Error: "you don't have permission to do that",
			})
			return
		}
		c.Next()
	}
}
