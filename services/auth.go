package services

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/lomokwa/mc-manager/db"
	"github.com/lomokwa/mc-manager/mail"
	"github.com/lomokwa/mc-manager/types"
	"golang.org/x/crypto/bcrypt"
)

func ForgotPassword(req types.ForgotPasswordRequest) error {
	var userID int
	err := db.DB.QueryRow("SELECT id FROM users WHERE email = ?", req.Email).Scan(&userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil // caller sends the same generic response either way
		}
		return err
	}

	tokenBytes := make([]byte, 32)

	if _, err := rand.Read(tokenBytes); err != nil {
		slog.Error("failed to generate reset token", "err", err)
		return err
	}
	token := hex.EncodeToString(tokenBytes)

	sum := sha256.Sum256(tokenBytes)
	tokenHash := hex.EncodeToString(sum[:])

	expiresAt := time.Now().UTC().Add(15 * time.Minute)

	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("UPDATE reset_password SET used_at = CURRENT_TIMESTAMP WHERE user_id = ? AND used_at IS NULL", userID); err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO reset_password (token_hash, expires_at, user_id) VALUES (?,?,?)", tokenHash, expiresAt, userID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	clientURL := os.Getenv("CLIENT_URL")
	link := fmt.Sprintf("%s/reset-password?token=%s", clientURL, token)

	go func() {
		if err := mail.SendPasswordReset(req.Email, link, expiresAt); err != nil {
			slog.Error("failed to send reset email", "user_id", userID, "err", err)
		}
	}()

	return nil
}

var ErrInvalidToken = errors.New("invalid or expired token")

const bcryptCost = 12

func ResetPassword(req types.ResetPasswordRequest) error {
	raw, err := hex.DecodeString(req.Token)
	if err != nil || len(raw) != 32 {
		return ErrInvalidToken
	}

	sum := sha256.Sum256(raw)
	tokenHash := hex.EncodeToString(sum[:])

	var userID int
	err = db.DB.QueryRow(
		"SELECT user_id FROM reset_password WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?",
		tokenHash, time.Now().UTC(),
	).Scan(&userID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Error("reset token lookup failed", "err", err)
		}
		return ErrInvalidToken
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcryptCost)
	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}

	tx, err := db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		"UPDATE reset_password SET used_at = CURRENT_TIMESTAMP WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?",
		tokenHash, time.Now().UTC(),
	)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrInvalidToken
	}

	if _, err := tx.Exec("UPDATE users SET password_hash = ? WHERE id = ?", string(hash), userID); err != nil {
		return err
	}

	if _, err := tx.Exec(
		"UPDATE reset_password SET used_at = CURRENT_TIMESTAMP WHERE user_id = ? AND used_at IS NULL",
		userID,
	); err != nil {
		return err
	}

	return tx.Commit()
}
