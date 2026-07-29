package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/lomokwa/mc-manager/types"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func makeJWT(t *testing.T, secret string, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("failed to sign test token: %v", err)
	}
	return signed
}

func TestValidateAPIKey_MissingKey(t *testing.T) {
	r := gin.New()
	r.Use(ValidateAPIKey())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, types.APIResponse{Success: true})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}

	var resp types.APIResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error != "missing API key" {
		t.Errorf("expected 'missing API key', got %q", resp.Error)
	}
}

func TestValidateAPIKey_InvalidKey(t *testing.T) {
	os.Setenv("API_KEY", "correct-key")
	defer os.Unsetenv("API_KEY")

	r := gin.New()
	r.Use(ValidateAPIKey())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, types.APIResponse{Success: true})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-API-Key", "wrong-key")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}

	var resp types.APIResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error != "invalid API key" {
		t.Errorf("expected 'invalid API key', got %q", resp.Error)
	}
}

func TestValidateAPIKey_ValidKey(t *testing.T) {
	os.Setenv("API_KEY", "correct-key")
	defer os.Unsetenv("API_KEY")

	r := gin.New()
	r.Use(ValidateAPIKey())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, types.APIResponse{Success: true})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-API-Key", "correct-key")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp types.APIResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Success {
		t.Error("expected success to be true")
	}
}

func TestValidateJWT_MissingAuth(t *testing.T) {
	r := gin.New()
	r.Use(ValidateJWT())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, types.APIResponse{Success: true})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
	var resp types.APIResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error != "missing Authorization header" {
		t.Errorf("expected 'missing Authorization header', got %q", resp.Error)
	}
}

func TestValidateJWT_MalformedAuthHeader(t *testing.T) {
	r := gin.New()
	r.Use(ValidateJWT())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, types.APIResponse{Success: true})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "NotBearer sometoken")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
	var resp types.APIResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error != "invalid Authorization format" {
		t.Errorf("expected 'invalid Authorization format', got %q", resp.Error)
	}
}

func TestValidateJWT_InvalidToken(t *testing.T) {
	os.Setenv("JWT_SECRET", "test-secret")
	defer os.Unsetenv("JWT_SECRET")

	r := gin.New()
	r.Use(ValidateJWT())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, types.APIResponse{Success: true})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
	var resp types.APIResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error != "invalid or expired token" {
		t.Errorf("expected 'invalid or expired token', got %q", resp.Error)
	}
}

func TestValidateJWT_ExpiredToken(t *testing.T) {
	os.Setenv("JWT_SECRET", "test-secret")
	defer os.Unsetenv("JWT_SECRET")

	token := makeJWT(t, "test-secret", jwt.MapClaims{
		"user_id": 1, "username": "alice",
		"exp": time.Now().Add(-time.Hour).Unix(),
	})

	r := gin.New()
	r.Use(ValidateJWT())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, types.APIResponse{Success: true})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestValidateJWT_WrongSigningSecret(t *testing.T) {
	os.Setenv("JWT_SECRET", "correct-secret")
	defer os.Unsetenv("JWT_SECRET")

	token := makeJWT(t, "wrong-secret", jwt.MapClaims{
		"user_id": 1, "username": "alice",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	r := gin.New()
	r.Use(ValidateJWT())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, types.APIResponse{Success: true})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestValidateJWT_ValidTokenViaHeader(t *testing.T) {
	os.Setenv("JWT_SECRET", "test-secret")
	defer os.Unsetenv("JWT_SECRET")

	token := makeJWT(t, "test-secret", jwt.MapClaims{
		"user_id": float64(42), "username": "alice",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	r := gin.New()
	r.Use(ValidateJWT())
	r.GET("/test", func(c *gin.Context) {
		userID, _ := c.Get("userID")
		username, _ := c.Get("username")
		c.JSON(http.StatusOK, gin.H{"userID": userID, "username": username})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["username"] != "alice" {
		t.Errorf("expected username=alice in context, got %+v", resp)
	}
	if resp["userID"] != float64(42) {
		t.Errorf("expected userID=42 in context, got %+v", resp)
	}
}

func TestValidateJWT_ValidTokenViaQueryParam(t *testing.T) {
	os.Setenv("JWT_SECRET", "test-secret")
	defer os.Unsetenv("JWT_SECRET")

	token := makeJWT(t, "test-secret", jwt.MapClaims{
		"user_id": float64(1), "username": "bob",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	r := gin.New()
	r.Use(ValidateJWT())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, types.APIResponse{Success: true})
	})

	req := httptest.NewRequest("GET", "/test?token="+token, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestValidateAPIKeyOrJWT_ValidAPIKey(t *testing.T) {
	os.Setenv("API_KEY", "correct-key")
	defer os.Unsetenv("API_KEY")

	r := gin.New()
	r.Use(ValidateAPIKeyOrJWT())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, types.APIResponse{Success: true})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-API-Key", "correct-key")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}
}

func TestValidateAPIKeyOrJWT_ValidJWTFallback(t *testing.T) {
	os.Setenv("API_KEY", "correct-key")
	os.Setenv("JWT_SECRET", "test-secret")
	defer os.Unsetenv("API_KEY")
	defer os.Unsetenv("JWT_SECRET")

	token := makeJWT(t, "test-secret", jwt.MapClaims{
		"user_id": float64(1), "username": "carol",
		"exp": time.Now().Add(time.Hour).Unix(),
	})

	r := gin.New()
	r.Use(ValidateAPIKeyOrJWT())
	r.GET("/test", func(c *gin.Context) {
		username, _ := c.Get("username")
		c.JSON(http.StatusOK, gin.H{"username": username})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}
	var resp map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["username"] != "carol" {
		t.Errorf("expected username=carol, got %+v", resp)
	}
}

func TestValidateAPIKeyOrJWT_InvalidAuthorizationFormat(t *testing.T) {
	os.Setenv("API_KEY", "correct-key")
	defer os.Unsetenv("API_KEY")

	r := gin.New()
	r.Use(ValidateAPIKeyOrJWT())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, types.APIResponse{Success: true})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("Authorization", "NotBearer sometoken")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	var resp types.APIResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error != "invalid Authorization format" {
		t.Errorf("expected 'invalid Authorization format', got %q", resp.Error)
	}
}

func TestValidateAPIKeyOrJWT_NeitherProvided(t *testing.T) {
	os.Setenv("API_KEY", "correct-key")
	defer os.Unsetenv("API_KEY")

	r := gin.New()
	r.Use(ValidateAPIKeyOrJWT())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, types.APIResponse{Success: true})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	var resp types.APIResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error != "valid API key or JWT required" {
		t.Errorf("expected 'valid API key or JWT required', got %q", resp.Error)
	}
}

func TestValidateAPIKeyOrJWT_InvalidBoth(t *testing.T) {
	os.Setenv("API_KEY", "correct-key")
	os.Setenv("JWT_SECRET", "test-secret")
	defer os.Unsetenv("API_KEY")
	defer os.Unsetenv("JWT_SECRET")

	r := gin.New()
	r.Use(ValidateAPIKeyOrJWT())
	r.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, types.APIResponse{Success: true})
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-API-Key", "wrong-key")
	req.Header.Set("Authorization", "Bearer not-a-real-token")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}
