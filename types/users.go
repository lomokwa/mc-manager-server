package types

import "time"

type User struct {
	ID        int    `json:"id"`
	Username  string `json:"username"`
	CreatedAt string `json:"created_at"`
}
type Invitation struct {
	Token     string    `json:"token"`
	Link      string    `json:"link"`
	ExpiresAt time.Time `json:"expires_at"`
}
