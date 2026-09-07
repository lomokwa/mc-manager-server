package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/lomokwa/mc-manager/services"
	"github.com/lomokwa/mc-manager/types"
)

// RecordActivity logs state-changing requests to the audit trail.
//
// Only mutations: a GET is someone looking, and recording every poll would
// bury the handful of rows that matter under thousands of "read the status"
// entries. The panel polls /api/status every 5s per open tab.
//
// This does NOT cover bans, kicks, op/de-op or whitelist changes -- the player
// panel sends those as console commands over the WebSocket, not as REST calls,
// so they are recorded in handlers/console.go instead. Between the two, the
// question the page exists to answer ("who banned this player?") is covered.
func RecordActivity() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		if isReadOnly(c.Request.Method) {
			return
		}
		category, action := classifyRequest(c.Request.Method, c.FullPath())
		if action == "" {
			return // not something worth a row
		}

		userID, _ := UserIDFromContext(c)
		username, _ := UsernameFromContext(c)
		if username == "" {
			// The admin routes accept an API key, which has no user behind it.
			username = "api key"
		}

		serverID := ""
		if rt, ok := RuntimeFromContext(c); ok && rt != nil {
			serverID = rt.ID
		}

		services.RecordActivity(types.ActivityEntry{
			UserID:   userID,
			Username: username,
			Category: category,
			Action:   action,
			// Deliberately the ROUTE PATTERN, never c.Request.URL: the API key
			// travels as ?key= and the console JWT as ?token=, so storing a URL
			// would make this table a credential leak. The pattern also groups
			// cleanly -- every file write reads as PUT /api/files.
			Detail:   c.Request.Method + " " + c.FullPath(),
			Status:   c.Writer.Status(),
			ServerID: serverID,
		})
	}
}

func isReadOnly(method string) bool {
	return method == "GET" || method == "HEAD" || method == "OPTIONS"
}

// classifyRequest maps a route to the category and the short verb the Activity
// page shows. Anything unmapped returns "" and is not recorded, so a new route
// is silently absent rather than silently mislabelled -- adding it here is a
// deliberate step.
func classifyRequest(method, route string) (category, action string) {
	// Refuse anything that looks like a real URL rather than a route pattern.
	// c.FullPath() is always a pattern, so this never fires in practice -- it
	// is here so the "no credential can reach the detail column" property holds
	// by construction instead of by a convention about who calls this. The API
	// key travels as ?key= and the console JWT as ?token=.
	if strings.ContainsAny(route, "?#") {
		return "", ""
	}

	// Namespaced and flat routes are the same action; the server it applied to
	// is already carried separately in server_id.
	route = stripServerPrefix(route)

	switch {
	case route == "/api/start":
		return types.ActivityServer, "started the server"
	case route == "/api/stop":
		return types.ActivityServer, "stopped the server"
	case route == "/api/server" && method == "POST":
		return types.ActivityServer, "created the server"
	case route == "/api/server" && method == "DELETE":
		return types.ActivityServer, "deleted the server"

	case route == "/api/properties":
		return types.ActivitySettings, "changed server settings"

	case strings.HasPrefix(route, "/api/files"):
		switch method {
		case "POST":
			return types.ActivityFiles, "uploaded a file"
		case "PUT":
			return types.ActivityFiles, "edited a file"
		case "DELETE":
			return types.ActivityFiles, "deleted a file"
		}

	case route == "/api/backups" && method == "POST":
		return types.ActivityBackups, "created a backup"
	case route == "/api/backups" && method == "DELETE":
		return types.ActivityBackups, "deleted a backup"
	case route == "/api/backups/restore":
		return types.ActivityBackups, "restored a backup"
	case route == "/api/backups/config":
		return types.ActivityBackups, "changed the backup schedule"

	case route == "/api/users/:id/role":
		return types.ActivityAccess, "changed someone's role"
	case route == "/api/users/:id/overrides":
		return types.ActivityAccess, "changed someone's permissions"
	case route == "/api/admin/invitations":
		return types.ActivityAccess, "created an invitation"
	case strings.HasPrefix(route, "/api/me/mclink"):
		return types.ActivityAccess, "changed their Minecraft account link"
	}
	return "", ""
}

// stripServerPrefix turns /api/servers/:sid/start into /api/start so the two
// route families classify identically.
func stripServerPrefix(route string) string {
	const prefix = "/api/servers/:sid"
	if rest, ok := strings.CutPrefix(route, prefix); ok && rest != "" {
		return "/api" + rest
	}
	return route
}
