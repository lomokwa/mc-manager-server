//go:build windows

package services

import (
	"fmt"
	"syscall"
	"unsafe"
)

// DiskPercentUsed reports how full the volume holding path is, 0-100.
//
// Production is Linux; this exists so a developer running the API on Windows
// gets the same behaviour rather than a build tag hole. It calls
// GetDiskFreeSpaceExW through the already-linked kernel32 instead of adding
// golang.org/x/sys as a dependency for one call.
func DiskPercentUsed(path string) (float64, error) {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getDiskFreeSpaceEx := kernel32.NewProc("GetDiskFreeSpaceExW")

	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, fmt.Errorf("invalid path: %w", err)
	}

	// freeToCaller is what this process may actually use; total is the volume.
	var freeToCaller, total, totalFree uint64
	r, _, callErr := getDiskFreeSpaceEx.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(&freeToCaller)),
		uintptr(unsafe.Pointer(&total)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if r == 0 {
		return 0, fmt.Errorf("stat volume: %w", callErr)
	}
	if total == 0 {
		return 0, fmt.Errorf("volume reports zero capacity")
	}

	used := total - freeToCaller
	return float64(used) / float64(total) * 100, nil
}
