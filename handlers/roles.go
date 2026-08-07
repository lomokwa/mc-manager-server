package handlers

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/lomokwa/mc-manager/middleware"
	"github.com/lomokwa/mc-manager/services"
	"github.com/lomokwa/mc-manager/types"
)

// ownerRoleName can never be assigned or edited through the API. There is
// exactly one path to it: the seed file dropped onto the server before it
// first boots (see services/seed.go). This is what keeps the server's
// original owner from ever being lockable-out or reassignable by a UI
// mistake or a compromised Admin account.
const ownerRoleName = "Owner"

// PermissionSchemaHandler returns the full permission catalogue (zones,
// keys, labels, descriptions) the client renders every checklist from.
func PermissionSchemaHandler(c *gin.Context) {
	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: types.PermissionSchema})
}

// MyPermissionsHandler returns the caller's own role and effective
// permissions. Any authenticated user may call this for themselves; it's
// what powers the read-only "My Permissions" section of the Account page.
func MyPermissionsHandler(c *gin.Context) {
	userID, ok := middleware.UserIDFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, types.APIResponse{Error: "missing or invalid session"})
		return
	}
	up, err := services.EffectivePermissions(userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: "failed to load permissions"})
		return
	}
	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: gin.H{
		"role":        up.RoleName,
		"permissions": up.Permissions,
	}})
}

// ListRolesHandler returns every role, including Owner -- shown so an admin
// can see what it grants, even though it can't be assigned from here.
func ListRolesHandler(c *gin.Context) {
	roles, err := services.ListRoles()
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: "failed to load roles"})
		return
	}
	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: roles})
}

// GetUserPermissionsHandler returns a target user's role and effective
// permissions, for the admin RolePanel.
func GetUserPermissionsHandler(c *gin.Context) {
	targetID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "invalid user id"})
		return
	}
	user, err := services.GetUserByID(targetID)
	if err != nil {
		c.JSON(http.StatusNotFound, types.APIResponse{Error: "user not found"})
		return
	}
	up, err := services.EffectivePermissions(targetID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: "failed to load permissions"})
		return
	}
	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: gin.H{
		"user_id":     user.ID,
		"username":    user.Username,
		"role":        up.RoleName,
		"permissions": up.Permissions,
	}})
}

type setRoleRequest struct {
	Role string `json:"role"`
}

// SetUserRoleHandler assigns a role to a user, replacing any previous role
// and clearing overrides. Refuses to ever assign Owner, and refuses to let an
// admin strip their own admin.manage_roles access -- both are lockout guards,
// not permission checks (the caller already passed RequirePermission to
// reach here).
func SetUserRoleHandler(c *gin.Context) {
	targetID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "invalid user id"})
		return
	}
	var req setRoleRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Role == "" {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "a role name is required"})
		return
	}
	if req.Role == ownerRoleName {
		c.JSON(http.StatusForbidden, types.APIResponse{
			Error: "Owner can't be assigned here -- it's set once via the server's seed file",
		})
		return
	}
	if _, err := services.GetUserByID(targetID); err != nil {
		c.JSON(http.StatusNotFound, types.APIResponse{Error: "user not found"})
		return
	}

	_, roleDefaults, _, err := services.GetRoleByName(req.Role)
	if err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "unknown role"})
		return
	}
	if callerID, ok := middleware.UserIDFromContext(c); ok && callerID == targetID && !containsPermission(roleDefaults, types.PermAdminManageRoles) {
		c.JSON(http.StatusForbidden, types.APIResponse{
			Error: "you can't remove your own role-management access",
		})
		return
	}

	if err := services.SetUserRole(targetID, req.Role); err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, types.APIResponse{Success: true})
}

type setOverridesRequest struct {
	Overrides map[types.Permission]bool `json:"overrides"`
}

// SetUserOverridesHandler replaces a user's permission overrides outright.
// Same self-lockout guard as SetUserRoleHandler, computed against the
// resulting effective set (role defaults + the proposed overrides) rather
// than the role alone.
func SetUserOverridesHandler(c *gin.Context) {
	targetID, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "invalid user id"})
		return
	}
	var req setOverridesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "invalid request body"})
		return
	}
	if _, err := services.GetUserByID(targetID); err != nil {
		c.JSON(http.StatusNotFound, types.APIResponse{Error: "user not found"})
		return
	}

	current, err := services.EffectivePermissions(targetID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: "failed to load current permissions"})
		return
	}
	if current.RoleName == ownerRoleName {
		c.JSON(http.StatusForbidden, types.APIResponse{Error: "Owner's permissions can't be edited"})
		return
	}

	if callerID, ok := middleware.UserIDFromContext(c); ok && callerID == targetID {
		_, roleDefaults, _, err := services.GetRoleByName(current.RoleName)
		if err == nil {
			resulting := map[types.Permission]bool{}
			for _, p := range roleDefaults {
				resulting[p] = true
			}
			for p, allowed := range req.Overrides {
				resulting[p] = allowed
			}
			if !resulting[types.PermAdminManageRoles] {
				c.JSON(http.StatusForbidden, types.APIResponse{
					Error: "you can't remove your own role-management access",
				})
				return
			}
		}
	}

	if err := services.SetUserOverrides(targetID, req.Overrides); err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, types.APIResponse{Success: true})
}

func containsPermission(perms []types.Permission, target types.Permission) bool {
	for _, p := range perms {
		if p == target {
			return true
		}
	}
	return false
}
