package types

type CreateServerRequest struct {
	ServerType          string            `json:"serverType" binding:"required"`
	ReleaseVersion      string            `json:"releaseVersion"`
	LoaderVersion       string            `json:"loaderVersion"`
	CreateLaunchScript  bool              `json:"createLaunchScript"`
	ConfigureProperties bool              `json:"configureProperties"`
	Properties          map[string]string `json:"properties"`
}

type StartServerRequest struct {
}

type UpdateServerPropertiesRequest struct {
	Properties map[string]string `json:"properties"`
}

type RegisterRequest struct {
	Token    string `json:"token" binding:"required"`
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// UpdateProfileRequest carries a caller's edits to their own profile.
// DisplayName is the only field so far; an empty string clears it back to
// falling through to Username on display.
type UpdateProfileRequest struct {
	DisplayName string `json:"display_name"`
}

// UpdateEmailRequest carries a caller's edit to their own email address, a
// separate endpoint from UpdateProfileRequest (rather than a shared field)
// so saving one doesn't require resending the other -- both use the
// empty-string-clears-it convention.
type UpdateEmailRequest struct {
	Email string `json:"email"`
}

// ChangePasswordRequest carries a caller's request to rotate their own
// password; CurrentPassword must verify against the stored hash before
// NewPassword is accepted, mirroring the login flow's verification step.
type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password" binding:"required"`
	NewPassword     string `json:"new_password" binding:"required"`
}
