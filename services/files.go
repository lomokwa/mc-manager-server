package services

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lomokwa/mc-manager/types"
)

// maxEditableFileSize caps how large a file may be to read or write as text.
const maxEditableFileSize = 2 << 20 // 2 MiB

// resolvePath maps a client-supplied path (relative to the server directory)
// to an absolute path guaranteed to stay inside ServerDir, defeating
// path-traversal attempts ("..", absolute paths, backslashes).
func resolvePath(rel string) (string, error) {
	base, err := filepath.Abs(ServerDir)
	if err != nil {
		return "", err
	}
	// Prefixing "/" then cleaning collapses any ".." so it can't climb above
	// the root; Join then re-roots the result inside the server directory.
	cleaned := filepath.Clean("/" + filepath.ToSlash(rel))
	abs, err := filepath.Abs(filepath.Join(base, cleaned))
	if err != nil {
		return "", err
	}
	if abs != base && !strings.HasPrefix(abs, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes the server directory")
	}
	return abs, nil
}

// ListFiles returns the entries of a directory (directories first, then names).
func ListFiles(rel string) ([]types.FileEntry, error) {
	abs, err := resolvePath(rel)
	if err != nil {
		return nil, err
	}
	dir, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	out := make([]types.FileEntry, 0, len(dir))
	for _, e := range dir {
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, types.FileEntry{
			Name:    e.Name(),
			IsDir:   e.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// ReadFileText returns the contents of a small text file.
func ReadFileText(rel string) (string, error) {
	abs, err := resolvePath(rel)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("path is a directory")
	}
	if info.Size() > maxEditableFileSize {
		return "", fmt.Errorf("file is too large to edit (%d bytes)", info.Size())
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// WriteFileText writes text content to a file inside the server directory.
func WriteFileText(rel string, content string) error {
	abs, err := resolvePath(rel)
	if err != nil {
		return err
	}
	if info, err := os.Stat(abs); err == nil && info.IsDir() {
		return fmt.Errorf("path is a directory")
	}
	return os.WriteFile(abs, []byte(content), 0644)
}

// ResolveDownloadPath returns the absolute path of an existing file to stream.
func ResolveDownloadPath(rel string) (string, error) {
	abs, err := resolvePath(rel)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("path is a directory")
	}
	return abs, nil
}

// SaveUpload writes an uploaded file into relDir under a sanitized base name.
func SaveUpload(relDir, name string, src io.Reader) error {
	name = filepath.Base(filepath.ToSlash(name))
	if name == "." || name == ".." || name == "" {
		return fmt.Errorf("invalid file name")
	}
	abs, err := resolvePath(filepath.Join(relDir, name))
	if err != nil {
		return err
	}
	out, err := os.Create(abs)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, src)
	return err
}
