package middleware

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/lomokwa/mc-manager/types"
)

// apiKeyValid reports whether the presented key matches API_KEY using a
// constant-time comparison, so the key can't be recovered through response
// timing. An empty/unset API_KEY fails closed (no key can be valid).
func apiKeyValid(presented string) bool {
	expected := os.Getenv("API_KEY")
	return expected != "" && subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) == 1
}

func ValidateAPIKey() gin.HandlerFunc {
	return func(c *gin.Context) {
		apiKey := c.Request.Header.Get("X-API-Key")
		if apiKey == "" {
			apiKey = c.Query("key")
		}

		if apiKey == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, types.APIResponse{Error: "missing API key"})
			return
		}

		if !apiKeyValid(apiKey) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, types.APIResponse{Error: "invalid API key"})
			return
		}

		c.Next()
	}
}

func ValidateJWT() gin.HandlerFunc {
	return func(c *gin.Context) {
		var tokenString string

		// Check Authorization header first
		authHeader := c.GetHeader("Authorization")
		if authHeader != "" {
			tokenString = strings.TrimPrefix(authHeader, "Bearer ")
			if tokenString == authHeader {
				c.AbortWithStatusJSON(http.StatusUnauthorized, types.APIResponse{Error: "invalid Authorization format"})
				return
			}
		} else if t := c.Query("token"); t != "" {
			// Fallback to query param (for WebSocket connections)
			tokenString = t
		} else {
			c.AbortWithStatusJSON(http.StatusUnauthorized, types.APIResponse{Error: "missing Authorization header"})
			return
		}

		// Parse and validate
		token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
			return []byte(os.Getenv("JWT_SECRET")), nil
		})
		if err != nil || !token.Valid {
			c.AbortWithStatusJSON(http.StatusUnauthorized, types.APIResponse{Error: "invalid or expired token"})
			return
		}

		// Extract claims and set in context
		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, types.APIResponse{Error: "invalid token claims"})
			return
		}

		c.Set("userID", claims["user_id"])
		c.Set("username", claims["username"])
		c.Next()
	}
}

func ValidateAPIKeyOrJWT() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Validate API key first, if not fallback to JWT
		apiKey := c.Request.Header.Get("X-API-Key")
		if apiKey == "" {
			apiKey = c.Query("key")
		}
		if apiKey != "" && apiKeyValid(apiKey) {
			c.Next()
			return
		}

		var tokenString string
		authHeader := c.GetHeader("Authorization")
		if authHeader != "" {
			tokenString = strings.TrimPrefix(authHeader, "Bearer ")
			if tokenString == authHeader {
				c.AbortWithStatusJSON(http.StatusUnauthorized, types.APIResponse{Error: "invalid Authorization format"})
				return
			}
		} else if t := c.Query("token"); t != "" {
			tokenString = t
		}

		if tokenString != "" {
			token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
				return []byte(os.Getenv("JWT_SECRET")), nil
			})
			if err == nil && token.Valid {
				claims, ok := token.Claims.(jwt.MapClaims)
				if ok {
					c.Set("userID", claims["user_id"])
					c.Set("username", claims["username"])
					c.Next()
					return
				}
			}
		}

		c.AbortWithStatusJSON(http.StatusUnauthorized, types.APIResponse{Error: "valid API key or JWT required"})
	}
}
