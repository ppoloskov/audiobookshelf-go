package httpx

import (
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
)

// ServeFile answers with the file at path, honouring Range and conditional
// requests (http.ServeContent). A missing file or a directory is a 404.
func ServeFile(w http.ResponseWriter, r *http.Request, path string) error {
	return serveFile(w, r, path, "")
}

// Download is ServeFile with a Content-Disposition attachment header naming
// the file filename (non-ASCII names use the RFC 2231 form).
func Download(w http.ResponseWriter, r *http.Request, path, filename string) error {
	return serveFile(w, r, path, filename)
}

func serveFile(w http.ResponseWriter, r *http.Request, path, filename string) error {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return NotFound()
		}
		return Internal("", fmt.Errorf("open file: %w", err))
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Internal("", fmt.Errorf("stat file: %w", err))
	}
	if info.IsDir() {
		return NotFound()
	}
	header := w.Header()
	if header.Get("ETag") == "" {
		header.Set("ETag", fmt.Sprintf(`W/"%x-%x"`, info.Size(), info.ModTime().UnixMilli()))
	}
	if filename != "" {
		disposition := mime.FormatMediaType("attachment", map[string]string{"filename": filename})
		if disposition == "" { // FormatMediaType rejects an unencodable name
			disposition = "attachment"
		}
		header.Set("Content-Disposition", disposition)
	}
	http.ServeContent(w, r, filepath.Base(path), info.ModTime(), file)
	return nil
}
