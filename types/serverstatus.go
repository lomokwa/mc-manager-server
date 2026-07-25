package types

import "time"

// ServerRuntimeStatus is written by mc-supervisor (the minecraft container) and
// read by the API (the mc-manager container) across the shared server
// directory, so the API can know the JVM's state without owning its process.
type ServerRuntimeStatus struct {
	Running   bool      `json:"running"`
	PID       int       `json:"pid"`
	Since     time.Time `json:"since"`
	Heartbeat time.Time `json:"heartbeat"`
	Desired   string    `json:"desired"`
}
