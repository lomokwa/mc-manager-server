package services

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/lomokwa/mc-manager/db"
	"github.com/lomokwa/mc-manager/types"
	"golang.org/x/crypto/bcrypt"
)

func CreateInvitation() (*types.Invitation, error) {

	// Generate secure random token
	tokenBytes := make([]byte, 32)

	if _, err := rand.Read(tokenBytes); err != nil {
		log.Printf("failed to generate invitation token: %v", err)
		return nil, err
	}
	token := hex.EncodeToString(tokenBytes)

	expiresAt := time.Now().Add(24 * time.Hour)

	query := "INSERT INTO invitations (token, expires_at) VALUES (?, ?)"
	_, err := db.DB.Exec(query, token, expiresAt)
	if err != nil {
		log.Printf("failed to insert invitation into database: %v", err)
		return nil, err
	}

	clientURL := os.Getenv("CLIENT_URL")
	link := fmt.Sprintf("%s/register?token=%s", clientURL, token)

	return &types.Invitation{
		Token:     token,
		Link:      link,
		ExpiresAt: expiresAt,
	}, nil

}

func ValidateInvitation(token string) error {
	var id int
	err := db.DB.QueryRow(
		"SELECT id FROM invitations WHERE token = ? AND used_at IS NULL AND expires_at > datetime('now')",
		token,
	).Scan(&id)
	if err != nil {
		return fmt.Errorf("invitation not found or expired")
	}
	return nil
}

func Register(req types.RegisterRequest) error {
	// Validate invitation token
	if err := ValidateInvitation(req.Token); err != nil {
		return err
	}

	// Hash password with bcrypt (cost 12)
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}

	// Create user
	_, err = db.DB.Exec(
		"INSERT INTO users (username, password_hash) VALUES (?, ?)",
		req.Username, string(hash),
	)
	if err != nil {
		return fmt.Errorf("failed to create user: %w", err)
	}

	// Mark invitation as used
	_, err = db.DB.Exec(
		"UPDATE invitations SET used_at = datetime('now') WHERE token = ?",
		req.Token,
	)
	if err != nil {
		return fmt.Errorf("failed to mark invitation as used: %w", err)
	}

	return nil
}

func Login(req types.LoginRequest) (string, error) {
	var passwordHash string
	var userID int
	err := db.DB.QueryRow(
		"SELECT id, password_hash FROM users WHERE username = ?",
		req.Username,
	).Scan(&userID, &passwordHash)
	if err != nil {
		return "", fmt.Errorf("invalid username or password")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(req.Password)); err != nil {
		return "", fmt.Errorf("invalid username or password")
	}

	// Generate JWT
	secret := os.Getenv("JWT_SECRET")
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"user_id":  userID,
		"username": req.Username,
		"exp":      time.Now().Add(24 * time.Hour).Unix(),
	})

	tokenString, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", fmt.Errorf("failed to generate token: %w", err)
	}

	return tokenString, nil
}

func GetUsers() ([]types.User, error) {
	rows, err := db.DB.Query("SELECT id, username, created_at FROM users")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []types.User
	for rows.Next() {
		var u types.User
		if err := rows.Scan(&u.ID, &u.Username, &u.CreatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}

	return users, nil
}
