package middleware

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/lomokwa/mc-manager/db"
	"github.com/lomokwa/mc-manager/services"
	"github.com/lomokwa/mc-manager/types"
)

// setupTestDB points db.DB at a fresh temp-file sqlite database, scoped to
// this package the same way services/handlers do it for theirs -- test
// helpers in _test.go files aren't shared across packages.
func setupTestDB(t *testing.T) {
	t.Helper()
	prev := db.DB
	dbPath := filepath.Join(t.TempDir(), "test.db")
	if err := db.Init(dbPath); err != nil {
		t.Fatalf("failed to init test db: %v", err)
	}
	t.Cleanup(func() {
		db.DB.Close()
		db.DB = prev
	})
}

func newPermissionTestUser(t *testing.T, role string) int {
	t.Helper()
	if err := services.EnsureBuiltinRoles(); err != nil {
		t.Fatalf("failed to seed roles: %v", err)
	}
	res, err := db.DB.Exec(`INSERT INTO users (username, password_hash) VALUES (?, ?)`, "test-user", "hash")
	if err != nil {
		t.Fatalf("failed to insert test user: %v", err)
	}
	id64, _ := res.LastInsertId()
	id := int(id64)
	if role != "" {
		if err := services.SetUserRole(id, role); err != nil {
			t.Fatalf("failed to assign role: %v", err)
		}
	}
	return id
}

func TestRequirePermission_Denied(t *testing.T) {
	setupTestDB(t)
	userID := newPermissionTestUser(t, "Viewer") // has console.read, not files.delete

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", float64(userID)) // matches how ValidateJWT stores a decoded JWT claim
		c.Next()
	})
	r.GET("/test", RequirePermission(types.PermFilesDelete), func(c *gin.Context) {
		c.JSON(http.StatusOK, types.APIResponse{Success: true})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", w.Code)
	}
}

func TestRequirePermission_Allowed(t *testing.T) {
	setupTestDB(t)
	userID := newPermissionTestUser(t, "Operator") // has server.start

	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set("userID", float64(userID))
		c.Next()
	})
	r.GET("/test", RequirePermission(types.PermServerStart), func(c *gin.Context) {
		c.JSON(http.StatusOK, types.APIResponse{Success: true})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestRequirePermission_NoUserIDInContext(t *testing.T) {
	setupTestDB(t)

	r := gin.New()
	// Deliberately no middleware setting "userID" -- simulates RequirePermission
	// being reached without ValidateJWT ever having run.
	r.GET("/test", RequirePermission(types.PermServerStart), func(c *gin.Context) {
		c.JSON(http.StatusOK, types.APIResponse{Success: true})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestUserIDFromContext_WrongType(t *testing.T) {
	r := gin.New()
	r.GET("/test", func(c *gin.Context) {
		c.Set("userID", "not-a-number")
		id, ok := UserIDFromContext(c)
		if ok {
			t.Errorf("expected ok=false for a non-float64 userID, got id=%d", id)
		}
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
}
