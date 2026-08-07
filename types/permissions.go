package types

// Permission is a single grantable capability. Values are stable strings
// (stored in roles.permissions / user_roles.overrides as JSON) rather than an
// int enum, so a role or override saved today still means the same thing
// after this list is reordered or extended.
type Permission string

const (
	PermServerStart Permission = "server.start"
	PermServerStop  Permission = "server.stop"

	PermConsoleRead     Permission = "console.read"
	PermConsoleChat     Permission = "console.chat"
	PermConsoleCommands Permission = "console.commands"

	PermFilesRead   Permission = "files.read"
	PermFilesUpload Permission = "files.upload"
	PermFilesEdit   Permission = "files.edit"
	PermFilesDelete Permission = "files.delete"

	PermBackupsView     Permission = "backups.view"
	PermBackupsCreate   Permission = "backups.create"
	PermBackupsDownload Permission = "backups.download"
	PermBackupsDelete   Permission = "backups.delete"
	PermBackupsRestore  Permission = "backups.restore"

	PermSettingsView Permission = "settings.view"
	PermSettingsEdit Permission = "settings.edit"

	PermPerformanceView   Permission = "performance.view"
	PermPerformanceReport Permission = "performance.report"

	PermPlayersView     Permission = "players.view"
	PermPlayersModerate Permission = "players.moderate"

	PermAdminManageUsers Permission = "admin.manage_users"
	PermAdminManageRoles Permission = "admin.manage_roles"
)

// PermissionInfo is one row in the schema the client renders a permission
// checklist from -- the label/description live here, once, instead of being
// duplicated in the frontend and drifting out of sync.
type PermissionInfo struct {
	Key         Permission `json:"key"`
	Label       string     `json:"label"`
	Description string     `json:"description"`
}

// PermissionZone groups related permissions for display (matches the app's
// existing "group by intent, not by widget" convention).
type PermissionZone struct {
	Key         string           `json:"key"`
	Label       string           `json:"label"`
	Permissions []PermissionInfo `json:"permissions"`
}

// PermissionSchema is the single source of truth for every permission that
// exists, its zone, and its display copy. The client fetches this once
// (GET /api/permissions/schema) instead of hardcoding a parallel list.
var PermissionSchema = []PermissionZone{
	{
		Key: "server", Label: "Server control",
		Permissions: []PermissionInfo{
			{PermServerStart, "Start server", "Boot the Minecraft server."},
			{PermServerStop, "Stop server", "Shut the Minecraft server down."},
		},
	},
	{
		Key: "console", Label: "Console",
		Permissions: []PermissionInfo{
			{PermConsoleRead, "Read console", "View the live console feed."},
			{PermConsoleChat, "Send chat messages", "Broadcast a message to all players (say)."},
			{PermConsoleCommands, "Send commands", "Run any other server command from the console."},
		},
	},
	{
		Key: "files", Label: "Files",
		Permissions: []PermissionInfo{
			{PermFilesRead, "Browse & download files", "View and download files inside the server directory."},
			{PermFilesUpload, "Upload files", "Add new files to the server directory."},
			{PermFilesEdit, "Edit files", "Change the contents of existing files."},
			{PermFilesDelete, "Delete files", "Remove files or folders."},
		},
	},
	{
		Key: "backups", Label: "Backups",
		Permissions: []PermissionInfo{
			{PermBackupsView, "View backups", "See the list of backups and the backup schedule."},
			{PermBackupsCreate, "Create backups", "Trigger a manual backup and change the automatic schedule."},
			{PermBackupsDownload, "Download backups", "Download a backup archive."},
			{PermBackupsDelete, "Delete backups", "Remove a backup archive."},
			{PermBackupsRestore, "Restore backups", "Replace the live world with a backup. The most destructive backup action."},
		},
	},
	{
		Key: "settings", Label: "Server settings",
		Permissions: []PermissionInfo{
			{PermSettingsView, "View settings", "See server.properties."},
			{PermSettingsEdit, "Edit settings", "Change server.properties."},
		},
	},
	{
		Key: "performance", Label: "Performance",
		Permissions: []PermissionInfo{
			{PermPerformanceView, "View performance", "See TPS, memory, and CPU on the Performance tab."},
			{PermPerformanceReport, "Generate reports", "Run spark profiler and health reports."},
		},
	},
	{
		Key: "players", Label: "Players",
		Permissions: []PermissionInfo{
			{PermPlayersView, "View players", "See the player roster and profiles."},
			{PermPlayersModerate, "Moderate players", "Op/de-op, kick, ban, and manage the whitelist."},
		},
	},
	{
		Key: "administration", Label: "Administration",
		Permissions: []PermissionInfo{
			{PermAdminManageUsers, "Manage users", "Invite and remove website accounts."},
			{PermAdminManageRoles, "Manage roles", "Assign roles and customize permissions for other users."},
		},
	},
}

// AllPermissions flattens the schema into a single list, e.g. for the Owner
// role's unrestricted default.
func AllPermissions() []Permission {
	var all []Permission
	for _, zone := range PermissionSchema {
		for _, p := range zone.Permissions {
			all = append(all, p.Key)
		}
	}
	return all
}

// RoleDefault is a built-in role's seed definition.
type RoleDefault struct {
	Name        string
	Permissions []Permission
}

// RoleInfo is a role as returned by the API: its stored permission set,
// decoded, plus whether it's one of the five built-in roles.
type RoleInfo struct {
	ID          int          `json:"id"`
	Name        string       `json:"name"`
	Permissions []Permission `json:"permissions"`
	IsSystem    bool         `json:"is_system"`
}

// BuiltinRoles are inserted (if missing, by name) on every boot by
// services.EnsureBuiltinRoles. Owner and Admin both hold every permission;
// what makes Owner different is that it can never be assigned, edited, or
// reached through the web UI (enforced in handlers/roles.go), only through
// the seed file dropped onto the server -- see services/seed.go.
var BuiltinRoles = []RoleDefault{
	{Name: "Owner", Permissions: AllPermissions()},
	{Name: "Admin", Permissions: AllPermissions()},
	{Name: "Moderator", Permissions: []Permission{
		PermConsoleRead, PermConsoleChat, PermConsoleCommands,
		PermPlayersView, PermPlayersModerate,
	}},
	{Name: "Operator", Permissions: []Permission{
		PermServerStart, PermServerStop,
		PermConsoleRead, PermConsoleChat,
		PermPlayersView,
	}},
	{Name: "Viewer", Permissions: []Permission{
		PermConsoleRead, PermPerformanceView, PermPlayersView,
	}},
}
