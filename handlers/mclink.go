package handlers

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lomokwa/mc-manager/db"
	"github.com/lomokwa/mc-manager/middleware"
	"github.com/lomokwa/mc-manager/services"
	"github.com/lomokwa/mc-manager/types"
)

// mcUsernameRE matches a legal Minecraft username. Enforced before the value
// is ever interpolated into a tellraw command string sent to the server
// console -- the actual injection guard, not just a UX nicety.
var mcUsernameRE = regexp.MustCompile(`^[A-Za-z0-9_]{1,16}$`)

// linkCodeCharset excludes 0/O and 1/I, which are easy to misread when
// copying a code from a game chat window into a browser tab.
const linkCodeCharset = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

const linkCodeTTL = 10 * time.Minute

func generateLinkCode() (string, error) {
	raw := make([]byte, 6)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	code := make([]byte, 6)
	for i, b := range raw {
		code[i] = linkCodeCharset[int(b)%len(linkCodeCharset)]
	}
	return string(code), nil
}

// findOnlinePlayer returns the real, correctly-cased name the server
// currently has for username (case-insensitive match), and whether it's
// online at all -- Minecraft commands need the exact case to resolve.
func findOnlinePlayer(username string) (string, bool) {
	online, err := services.GetOnlinePlayers()
	if err != nil {
		return "", false
	}
	for _, name := range online {
		if strings.EqualFold(name, username) {
			return name, true
		}
	}
	return "", false
}

type startMcLinkRequest struct {
	MinecraftUsername string `json:"mc_username"`
}

// StartMcLinkHandler generates a one-time code and delivers it to the named
// player privately in-game (a tellraw message only they see), so a website
// account can only be linked to a Minecraft account someone can actually
// play as right now.
func StartMcLinkHandler(c *gin.Context) {
	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, types.APIResponse{Error: "missing or invalid session"})
		return
	}
	var req startMcLinkRequest
	if err := c.ShouldBindJSON(&req); err != nil || !mcUsernameRE.MatchString(req.MinecraftUsername) {
		c.JSON(http.StatusBadRequest, types.APIResponse{
			Error: "enter a valid Minecraft username (letters, numbers, underscore, up to 16 characters)",
		})
		return
	}
	if !services.IsServerRunning() {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "the server needs to be running to receive the code"})
		return
	}
	realName, online := findOnlinePlayer(req.MinecraftUsername)
	if !online {
		c.JSON(http.StatusBadRequest, types.APIResponse{
			Error: fmt.Sprintf("%s needs to be online to receive the code", req.MinecraftUsername),
		})
		return
	}

	code, err := generateLinkCode()
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: "failed to generate a code"})
		return
	}
	expiresAt := time.Now().Add(linkCodeTTL)
	if _, err := db.DB.Exec(`
		INSERT INTO mc_link_codes (user_id, mc_username, code, expires_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET mc_username = excluded.mc_username, code = excluded.code, expires_at = excluded.expires_at`,
		userID, realName, code, expiresAt,
	); err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: "failed to start the link"})
		return
	}

	tellraw := fmt.Sprintf(
		`tellraw %s {"text":"[mc-manager] Your website link code is %s. It expires in 10 minutes.","color":"aqua"}`,
		realName, code,
	)
	if err := services.SendCommand(tellraw); err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: "failed to message the player in-game"})
		return
	}

	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: gin.H{
		"mc_username": realName,
		"expires_at":  expiresAt,
	}})
}

type verifyMcLinkRequest struct {
	Code string `json:"code"`
}

// VerifyMcLinkHandler completes a link: the code the user read in-game and
// typed back must match what StartMcLinkHandler stored, and not be expired.
func VerifyMcLinkHandler(c *gin.Context) {
	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, types.APIResponse{Error: "missing or invalid session"})
		return
	}
	var req verifyMcLinkRequest
	if err := c.ShouldBindJSON(&req); err != nil || strings.TrimSpace(req.Code) == "" {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "enter the code you received in-game"})
		return
	}

	var mcUsername, storedCode string
	var expiresAt time.Time
	err := db.DB.QueryRow(`SELECT mc_username, code, expires_at FROM mc_link_codes WHERE user_id = ?`, userID).
		Scan(&mcUsername, &storedCode, &expiresAt)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "start the link from your account page first"})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: "failed to check the code"})
		return
	}
	if time.Now().After(expiresAt) {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "that code expired, generate a new one"})
		return
	}
	if !strings.EqualFold(strings.TrimSpace(req.Code), storedCode) {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "that code doesn't match"})
		return
	}

	uuid, _ := services.LookupUUID(mcUsername)
	if _, err := db.DB.Exec(`
		INSERT INTO minecraft_links (user_id, mc_username, mc_uuid) VALUES (?, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET mc_username = excluded.mc_username, mc_uuid = excluded.mc_uuid, linked_at = CURRENT_TIMESTAMP`,
		userID, mcUsername, uuid,
	); err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: "failed to save the link"})
		return
	}
	db.DB.Exec(`DELETE FROM mc_link_codes WHERE user_id = ?`, userID)

	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: gin.H{
		"mc_username": mcUsername,
		"mc_uuid":     uuid,
	}})
}

// GetMcLinkHandler returns the caller's current link, if any.
func GetMcLinkHandler(c *gin.Context) {
	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, types.APIResponse{Error: "missing or invalid session"})
		return
	}
	var mcUsername, uuid string
	var linkedAt time.Time
	err := db.DB.QueryRow(`SELECT mc_username, mc_uuid, linked_at FROM minecraft_links WHERE user_id = ?`, userID).
		Scan(&mcUsername, &uuid, &linkedAt)
	if err == sql.ErrNoRows {
		c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: nil})
		return
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: "failed to load the link"})
		return
	}
	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: gin.H{
		"mc_username": mcUsername,
		"mc_uuid":     uuid,
		"linked_at":   linkedAt,
	}})
}

// UnlinkMcHandler removes the caller's own Minecraft link. Self-service --
// no special permission needed beyond being the account owner.
func UnlinkMcHandler(c *gin.Context) {
	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, types.APIResponse{Error: "missing or invalid session"})
		return
	}
	if _, err := db.DB.Exec(`DELETE FROM minecraft_links WHERE user_id = ?`, userID); err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: "failed to remove the link"})
		return
	}
	c.JSON(http.StatusOK, types.APIResponse{Success: true})
}
