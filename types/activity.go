package types

// ActivityEntry is one audit row: who did what, when, and to which server.
type ActivityEntry struct {
	ID        int    `json:"id"`
	CreatedAt string `json:"created_at"`
	// 0 for an API-key caller, which has no user account behind it.
	UserID   int    `json:"user_id,omitempty"`
	Username string `json:"username"`
	Category string `json:"category"`
	Action   string `json:"action"`
	Detail   string `json:"detail,omitempty"`
	// HTTP status for a REST action; 0 for a console command, which has none.
	Status   int    `json:"status,omitempty"`
	ServerID string `json:"server_id,omitempty"`
}

// Activity categories. These are the filter chips on the Activity page, so
// they are grouped by what someone is looking for ("who touched the files?"),
// not by which handler happened to serve the request.
const (
	ActivityServer   = "server"
	ActivityPlayers  = "players"
	ActivityFiles    = "files"
	ActivityBackups  = "backups"
	ActivitySettings = "settings"
	ActivityAccess   = "access"
	ActivityConsole  = "console"
)

// ActivityCategories is the display list, in the order the chips render.
var ActivityCategories = []string{
	ActivityServer, ActivityPlayers, ActivityConsole,
	ActivityFiles, ActivityBackups, ActivitySettings, ActivityAccess,
}
