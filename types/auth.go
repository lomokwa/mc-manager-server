package types

import "time"

type ForgotPassword struct {
	Token     string    `json:"token"`
	Link      string    `json:"link"`
	ExpiresAt time.Time `json:"expires_at"`
}
