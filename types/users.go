package types

import "time"

type User struct {
	ID          int    `json:"id"`
	Username    string `json:"username"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	// AvatarURL is a path relative to the API origin (e.g. "/avatars/3-ab12cd34.png"),
	// not an absolute URL -- the frontend is responsible for prefixing it with
	// whatever base URL it's already using to reach the API.
	AvatarURL string `json:"avatar_url,omitempty"`
	CreatedAt string `json:"created_at"`
}
type Invitation struct {
	Token     string    `json:"token"`
	Link      string    `json:"link"`
	ExpiresAt time.Time `json:"expires_at"`
}
