//go:build !windows

package services

import (
	"fmt"
	"syscall"
)

// DiskPercentUsed reports how full the filesystem holding path is, 0-100.
//
// Uses the volume's own accounting rather than walking directories: the number
// a disk-usage rule cares about is "will the next backup fit", and that is a
// property of the filesystem, not of the server directory's contents.
//
// Deliberately measures the whole volume. Worlds and backups share it, so a
// per-directory figure would miss exactly the case that matters -- backups
// filling the disk the world also lives on.
func DiskPercentUsed(path string) (float64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("stat filesystem: %w", err)
	}

	total := st.Blocks * uint64(st.Bsize)
	if total == 0 {
		return 0, fmt.Errorf("filesystem reports zero capacity")
	}
	// Bavail, not Bfree: the reserved blocks root can still use are not space
	// the server process will ever get, so counting them would understate how
	// full the disk is from the only perspective that matters here.
	free := st.Bavail * uint64(st.Bsize)
	used := total - free

	return float64(used) / float64(total) * 100, nil
}
