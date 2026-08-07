package services

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"github.com/lomokwa/mc-manager/db"
)

// PermissionsSeedPath is where the seed file lives once settled: inside the
// control-plane directory, which handlers/files.go's safePath already
// excludes from the file browser (it's how the FIFOs/status.json stay
// hidden from that page too) -- so this never shows up next to world data
// for anyone with files.read, and it survives container recreates since
// ControlDir is on the same bind-mounted volume as the rest of ServerDir.
var PermissionsSeedPath = filepath.Join(ControlDir, "permissions-seed.json")

// permissionsSeedFallbacks are checked in order, and the first one found is
// moved to PermissionsSeedPath -- never overwriting it if it's already
// there. ServerDir is first because it's the one location every operator
// can reach the same way they already reach server.properties (FTP/SFTP/a
// hosting panel's file manager), even without access to wherever this
// repo's own working directory actually lives. The repo root is kept as a
// fallback for anyone who *does* have that access (local dev, direct SSH).
var permissionsSeedFallbacks = []string{
	filepath.Join(ServerDir, "permissions-seed.json"),
	"./permissions-seed.json",
}

type seedEntry struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

type seedFile struct {
	Users []seedEntry `json:"users"`
}

// ApplyPermissionsSeed reads PermissionsSeedPath, if present, and assigns
// each listed username its role -- but only for a user who doesn't already
// have one. It runs on every boot rather than once, on purpose: the file is
// usually dropped before anyone has registered yet, so the intended owner's
// account may not exist on the first boot that sees it. Once a user has a
// role (assigned here or from the UI), this never touches them again, so
// hand-tuned overrides are never clobbered by a redeploy.
func ApplyPermissionsSeed() {
	settleSeedFile()

	data, err := os.ReadFile(PermissionsSeedPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("permissions seed: failed to read %s: %v", PermissionsSeedPath, err)
		}
		return
	}

	var seed seedFile
	if err := json.Unmarshal(data, &seed); err != nil {
		log.Printf("permissions seed: invalid JSON in %s: %v", PermissionsSeedPath, err)
		return
	}

	for _, entry := range seed.Users {
		applySeedEntry(entry)
	}
}

// settleSeedFile moves the first fallback file it finds into
// PermissionsSeedPath. A no-op the moment something is already at
// PermissionsSeedPath -- so a fallback copy left behind by a failed earlier
// move, or a second file dropped later, can never clobber whatever's
// already settled and possibly already applied.
func settleSeedFile() {
	if _, err := os.Stat(PermissionsSeedPath); err == nil {
		return
	}

	for _, candidate := range permissionsSeedFallbacks {
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		if err := os.MkdirAll(ControlDir, 0o770); err != nil {
			log.Printf("permissions seed: failed to create %s: %v", ControlDir, err)
			return
		}
		if err := os.Rename(candidate, PermissionsSeedPath); err != nil {
			log.Printf("permissions seed: failed to move %s to %s: %v", candidate, PermissionsSeedPath, err)
			return
		}
		log.Printf("permissions seed: moved %s to %s", candidate, PermissionsSeedPath)
		return
	}
}

// EnsureBootstrapOwner is a last-resort safety net for the case
// permissions-seed.json wasn't dropped in place before this feature's first
// deploy: with role assignment being deny-by-default, that would otherwise
// leave every existing account (including whoever has actually been running
// this server) suddenly unable to do anything the instant it goes live. If
// literally no one has a role yet after the seed file has had its turn, the
// earliest-registered account becomes Owner -- imperfect (a second admin
// still needs assigning by hand, through the UI this unlocks), but never a
// silent total lockout. A no-op the moment anyone has a role, seeded or
// hand-assigned, so this can never override real configuration.
func EnsureBootstrapOwner() {
	var count int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM user_roles`).Scan(&count); err != nil {
		log.Printf("bootstrap owner: failed to check existing roles: %v", err)
		return
	}
	if count > 0 {
		return
	}

	var firstUserID int
	if err := db.DB.QueryRow(`SELECT id FROM users ORDER BY id ASC LIMIT 1`).Scan(&firstUserID); err != nil {
		return // no one has registered yet -- nothing to bootstrap
	}
	if err := SetUserRole(firstUserID, "Owner"); err != nil {
		log.Printf("bootstrap owner: failed to assign role: %v", err)
		return
	}
	log.Printf("bootstrap: no roles existed yet, granted Owner to the first registered account (user id %d)", firstUserID)
}

func applySeedEntry(entry seedEntry) {
	var userID int
	if err := db.DB.QueryRow(`SELECT id FROM users WHERE username = ?`, entry.Username).Scan(&userID); err != nil {
		log.Printf("permissions seed: %q isn't registered yet, will retry next boot", entry.Username)
		return
	}

	var existing int
	err := db.DB.QueryRow(`SELECT user_id FROM user_roles WHERE user_id = ?`, userID).Scan(&existing)
	if err == nil {
		return // already has a role -- never overwritten by the seed
	}

	if _, _, _, err := GetRoleByName(entry.Role); err != nil {
		log.Printf("permissions seed: %q lists unknown role %q", entry.Username, entry.Role)
		return
	}
	if err := SetUserRole(userID, entry.Role); err != nil {
		log.Printf("permissions seed: failed to assign %q to %q: %v", entry.Role, entry.Username, err)
		return
	}
	log.Printf("permissions seed: assigned %q the %q role", entry.Username, entry.Role)
}
