package handlers

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lomokwa/mc-manager/services"
	"github.com/lomokwa/mc-manager/types"
)

type fileEntry struct {
	Name    string    `json:"name"`
	IsDir   bool      `json:"is_dir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// safePath resolves the requested path within the minecraft-server directory
// and prevents path traversal attacks.
func safePath(requested string) (string, error) {
	base, err := filepath.Abs(services.ServerDir)
	if err != nil {
		return "", err
	}

	target := filepath.Join(base, filepath.Clean("/"+requested))
	if !strings.HasPrefix(target, base) {
		return "", fmt.Errorf("path traversal denied")
	}
	return target, nil
}

// ListFilesHandler lists directory contents.
func ListFilesHandler(c *gin.Context) {
	reqPath := c.Query("path")

	resolved, err := safePath(reqPath)
	if err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: err.Error()})
		return
	}

	info, err := os.Stat(resolved)
	if err != nil {
		c.JSON(http.StatusNotFound, types.APIResponse{Success: false, Error: "path not found"})
		return
	}
	if !info.IsDir() {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: "not a directory"})
		return
	}

	dirEntries, err := os.ReadDir(resolved)
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Success: false, Error: "failed to read directory"})
		return
	}

	entries := make([]fileEntry, 0, len(dirEntries))
	for _, de := range dirEntries {
		info, err := de.Info()
		if err != nil {
			continue
		}
		entries = append(entries, fileEntry{
			Name:    de.Name(),
			IsDir:   de.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}

	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: entries})
}

// ReadFileHandler reads a file and returns its content as a string.
func ReadFileHandler(c *gin.Context) {
	reqPath := c.Query("path")

	resolved, err := safePath(reqPath)
	if err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: err.Error()})
		return
	}

	info, err := os.Stat(resolved)
	if err != nil {
		c.JSON(http.StatusNotFound, types.APIResponse{Success: false, Error: "file not found"})
		return
	}
	if info.IsDir() {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: "path is a directory"})
		return
	}

	// Reject files larger than 5MB to prevent loading huge binaries
	if info.Size() > 5*1024*1024 {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: "file too large to edit (>5MB)"})
		return
	}

	content, err := os.ReadFile(resolved)
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Success: false, Error: "failed to read file"})
		return
	}

	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: string(content)})
}

// WriteFileHandler writes content to an existing file.
func WriteFileHandler(c *gin.Context) {
	reqPath := c.Query("path")

	resolved, err := safePath(reqPath)
	if err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: err.Error()})
		return
	}

	info, err := os.Stat(resolved)
	if err != nil {
		c.JSON(http.StatusNotFound, types.APIResponse{Success: false, Error: "file not found"})
		return
	}
	if info.IsDir() {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: "path is a directory"})
		return
	}

	var body struct {
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: "invalid request body"})
		return
	}

	if err := os.WriteFile(resolved, []byte(body.Content), info.Mode()); err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Success: false, Error: "failed to write file"})
		return
	}

	c.JSON(http.StatusOK, types.APIResponse{Success: true})
}

// DownloadFileHandler serves a file as a binary download.
func DownloadFileHandler(c *gin.Context) {
	reqPath := c.Query("path")

	resolved, err := safePath(reqPath)
	if err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: err.Error()})
		return
	}

	info, err := os.Stat(resolved)
	if err != nil {
		c.JSON(http.StatusNotFound, types.APIResponse{Success: false, Error: "file not found"})
		return
	}
	if info.IsDir() {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: "cannot download a directory"})
		return
	}

	c.FileAttachment(resolved, filepath.Base(resolved))
}

// UploadFileHandler handles multipart file uploads to a directory.
func UploadFileHandler(c *gin.Context) {
	reqPath := c.Query("path")

	resolved, err := safePath(reqPath)
	if err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: err.Error()})
		return
	}

	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: "target must be a directory"})
		return
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: "no file provided"})
		return
	}
	defer file.Close()

	// Sanitize filename
	filename := filepath.Base(header.Filename)
	dest := filepath.Join(resolved, filename)

	// Ensure destination is still within the server dir
	if destCheck, err := safePath(filepath.Join(reqPath, filename)); err != nil || destCheck != dest {
		c.JSON(http.StatusBadRequest, types.APIResponse{Success: false, Error: "invalid filename"})
		return
	}

	out, err := os.Create(dest)
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Success: false, Error: "failed to create file"})
		return
	}
	defer out.Close()

	if _, err := io.Copy(out, file); err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Success: false, Error: "failed to save file"})
		return
	}

	c.JSON(http.StatusOK, types.APIResponse{Success: true})
}
