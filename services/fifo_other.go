//go:build !linux

package services

import "fmt"

// writeFifo is only meaningful where the server actually runs (Linux
// containers). This stub keeps `go build`/`go vet`/`go test` green on other
// platforms (e.g. a developer's Windows machine) without needing a build tag
// at every call site.
func writeFifo(path, payload string) error {
	return fmt.Errorf("FIFO-based server control is only supported on linux")
}
