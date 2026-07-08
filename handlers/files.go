package handlers

import (
	"net/http"
	"path/filepath"

	"github.com/gin-gonic/gin"
	"github.com/lomokwa/mc-manager/services"
	"github.com/lomokwa/mc-manager/types"
)

// ListFilesHandler godoc
// @Summary      List files
// @Description  Lists entries in a directory under the server directory.
// @Tags         files
// @Produce      json
// @Param        path  query  string  false  "Directory path relative to the server directory"
// @Success      200  {object}  types.APIResponse
// @Security     ApiKeyAuth
// @Router       /api/files [get]
func ListFilesHandler(c *gin.Context) {
	entries, err := services.ListFiles(c.Query("path"))
	if err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: entries})
}

// ReadFileHandler godoc
// @Summary      Read a text file
// @Tags         files
// @Produce      json
// @Param        path  query  string  true  "File path relative to the server directory"
// @Success      200  {object}  types.APIResponse
// @Security     ApiKeyAuth
// @Router       /api/files/read [get]
func ReadFileHandler(c *gin.Context) {
	content, err := services.ReadFileText(c.Query("path"))
	if err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, types.APIResponse{Success: true, Data: content})
}

// WriteFileHandler godoc
// @Summary      Save a text file
// @Tags         files
// @Accept       json
// @Produce      json
// @Param        path     query  string                     true  "File path relative to the server directory"
// @Param        request  body   types.WriteFileRequest     true  "File content"
// @Success      200  {object}  types.APIResponse
// @Security     ApiKeyAuth
// @Router       /api/files [put]
func WriteFileHandler(c *gin.Context) {
	var req types.WriteFileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "invalid request body"})
		return
	}
	if err := services.WriteFileText(c.Query("path"), req.Content); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, types.APIResponse{Success: true})
}

// DownloadFileHandler godoc
// @Summary      Download a file
// @Tags         files
// @Param        path  query  string  true  "File path relative to the server directory"
// @Success      200  {file}  binary
// @Security     ApiKeyAuth
// @Router       /api/files/download [get]
func DownloadFileHandler(c *gin.Context) {
	abs, err := services.ResolveDownloadPath(c.Query("path"))
	if err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: err.Error()})
		return
	}
	c.FileAttachment(abs, filepath.Base(abs))
}

// UploadFileHandler godoc
// @Summary      Upload a file
// @Tags         files
// @Accept       multipart/form-data
// @Produce      json
// @Param        path  query     string  false  "Destination directory relative to the server directory"
// @Param        file  formData  file    true   "File to upload"
// @Success      200  {object}  types.APIResponse
// @Security     ApiKeyAuth
// @Router       /api/files/upload [post]
func UploadFileHandler(c *gin.Context) {
	file, err := c.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: "no file provided"})
		return
	}
	src, err := file.Open()
	if err != nil {
		c.JSON(http.StatusInternalServerError, types.APIResponse{Error: err.Error()})
		return
	}
	defer src.Close()
	if err := services.SaveUpload(c.Query("path"), file.Filename, src); err != nil {
		c.JSON(http.StatusBadRequest, types.APIResponse{Error: err.Error()})
		return
	}
	c.JSON(http.StatusOK, types.APIResponse{Success: true})
}
