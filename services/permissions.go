package services

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lomokwa/mc-manager/db"
	"github.com/lomokwa/mc-manager/types"
)

// EnsureBuiltinRoles inserts the five built-in roles if a role with that name
// doesn't already exist. Safe to call on every boot: existing roles (and any
// hand-edited permission set on them) are left untouched.
func EnsureBuiltinRoles() error {
	for _, r := range types.BuiltinRoles {
		perms, err := json.Marshal(r.Permissions)
		if err != nil {
			return fmt.Errorf("marshal permissions for role %q: %w", r.Name, err)
		}
		if _, err := db.DB.Exec(
			`INSERT INTO roles (name, permissions, is_system) VALUES (?, ?, 1)
			 ON CONFLICT(name) DO NOTHING`,
			r.Name, string(perms),
		); err != nil {
			return fmt.Errorf("seed role %q: %w", r.Name, err)
		}
	}
	return nil
}

// UserPermissions is one user's resolved access: which role they hold (empty
// if none) and the final set of permissions after applying overrides on top
// of that role's defaults.
type UserPermissions struct {
	RoleName    string
	Permissions map[types.Permission]bool
}

// EffectivePermissions resolves a user's final permission set: their role's
// defaults with per-user overrides applied on top. A user with no role row
// has no permissions -- deny by default, not an error.
func EffectivePermissions(userID int) (UserPermissions, error) {
	var roleName, rolePermsJSON, overridesJSON string
	err := db.DB.QueryRow(`
		SELECT r.name, r.permissions, ur.overrides
		FROM user_roles ur JOIN roles r ON r.id = ur.role_id
		WHERE ur.user_id = ?`, userID,
	).Scan(&roleName, &rolePermsJSON, &overridesJSON)

	if err == sql.ErrNoRows {
		return UserPermissions{Permissions: map[types.Permission]bool{}}, nil
	}
	if err != nil {
		return UserPermissions{}, fmt.Errorf("load role for user %d: %w", userID, err)
	}

	var rolePerms []types.Permission
	if err := json.Unmarshal([]byte(rolePermsJSON), &rolePerms); err != nil {
		return UserPermissions{}, fmt.Errorf("decode role permissions: %w", err)
	}
	var overrides map[types.Permission]bool
	if err := json.Unmarshal([]byte(overridesJSON), &overrides); err != nil {
		return UserPermissions{}, fmt.Errorf("decode permission overrides: %w", err)
	}

	effective := make(map[types.Permission]bool, len(rolePerms))
	for _, p := range rolePerms {
		effective[p] = true
	}
	for p, allowed := range overrides {
		effective[p] = allowed
	}

	return UserPermissions{RoleName: roleName, Permissions: effective}, nil
}

// HasPermission is a convenience single-permission check. Any error (a
// missing DB, a corrupt row) resolves to false: a permission check that can't
// prove "yes" must answer "no".
func HasPermission(userID int, perm types.Permission) bool {
	up, err := EffectivePermissions(userID)
	if err != nil {
		return false
	}
	return up.Permissions[perm]
}

// GetRoleByName returns a role's stored permission set, used to validate a
// role-name assignment and to show role defaults in the admin UI.
func GetRoleByName(name string) (id int, permissions []types.Permission, isSystem bool, err error) {
	var permsJSON string
	err = db.DB.QueryRow(`SELECT id, permissions, is_system FROM roles WHERE name = ?`, name).
		Scan(&id, &permsJSON, &isSystem)
	if err != nil {
		return 0, nil, false, err
	}
	if err := json.Unmarshal([]byte(permsJSON), &permissions); err != nil {
		return 0, nil, false, fmt.Errorf("decode permissions for role %q: %w", name, err)
	}
	return id, permissions, isSystem, nil
}

// ListRoles returns every role, permissions decoded, for the admin role
// picker.
func ListRoles() ([]types.RoleInfo, error) {
	rows, err := db.DB.Query(`SELECT id, name, permissions, is_system FROM roles ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var roles []types.RoleInfo
	for rows.Next() {
		var r types.RoleInfo
		var permsJSON string
		if err := rows.Scan(&r.ID, &r.Name, &permsJSON, &r.IsSystem); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(permsJSON), &r.Permissions); err != nil {
			return nil, fmt.Errorf("decode permissions for role %q: %w", r.Name, err)
		}
		roles = append(roles, r)
	}
	return roles, rows.Err()
}

// SetUserRole assigns roleName to userID, replacing any previous role and
// clearing overrides (a fresh role starts from its own defaults; carrying a
// stale override forward could silently grant or deny something the admin
// didn't intend).
func SetUserRole(userID int, roleName string) error {
	roleID, _, _, err := GetRoleByName(roleName)
	if err != nil {
		return fmt.Errorf("role %q not found", roleName)
	}
	_, err = db.DB.Exec(`
		INSERT INTO user_roles (user_id, role_id, overrides) VALUES (?, ?, '{}')
		ON CONFLICT(user_id) DO UPDATE SET role_id = excluded.role_id, overrides = '{}'`,
		userID, roleID,
	)
	return err
}

// SetUserOverrides replaces userID's permission overrides outright (not a
// merge) -- the caller sends the full desired override map, matching how the
// admin UI's checklist always submits its complete current state.
func SetUserOverrides(userID int, overrides map[types.Permission]bool) error {
	data, err := json.Marshal(overrides)
	if err != nil {
		return err
	}
	res, err := db.DB.Exec(`UPDATE user_roles SET overrides = ? WHERE user_id = ?`, string(data), userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("user %d has no role assigned yet", userID)
	}
	return nil
}

// LookupUUID returns the UUID Minecraft has on file for a username, read from
// usercache.json (the same source ListPlayers uses) with a case-insensitive
// match. A player only appears here after joining at least once, which the
// account-link flow already requires since the code is delivered by
// messaging them in-game.
func LookupUUID(username string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(ServerDir, "usercache.json"))
	if err != nil {
		return "", false
	}
	var entries []types.UserCacheEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return "", false
	}
	for _, e := range entries {
		if strings.EqualFold(e.Name, username) {
			return e.UUID, true
		}
	}
	return "", false
}
