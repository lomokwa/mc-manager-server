package types

// FileEntry describes one item in a directory listing.
type FileEntry struct {
	Name    string `json:"name"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time"` // RFC3339
}

// WriteFileRequest is the body for saving a text file.
type WriteFileRequest struct {
	Content string `json:"content"`
}
