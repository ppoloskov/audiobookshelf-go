package backup

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/settings"
)

// Routes registers the backup routes on mux. Every route answers 403 to a
// user who is not an admin, before it looks at the backup.
func (h *Handler) Routes(mux *http.ServeMux) {
	admin := func(pattern string, f httpx.Handler) { mux.Handle(pattern, auth.Admin(f)) }
	// onBackup resolves {id} first; an unknown backup answers 404.
	onBackup := func(f func(http.ResponseWriter, *http.Request, Backup) error) httpx.Handler {
		return func(w http.ResponseWriter, r *http.Request) error {
			backups := h.list()
			i := slices.IndexFunc(backups, func(b Backup) bool { return b.ID == r.PathValue("id") })
			if i < 0 {
				return httpx.NotFound()
			}
			return f(w, r, backups[i])
		}
	}
	admin("GET /api/backups", h.listRoute)
	admin("POST /api/backups", h.createRoute)
	admin("POST /api/backups/upload", h.upload)
	admin("PATCH /api/backups/path", h.updatePath)
	admin("GET /api/backups/{id}/download", onBackup(func(w http.ResponseWriter, r *http.Request, b Backup) error {
		return httpx.Download(w, r, b.FullPath, filepath.Base(b.Filename))
	}))
	admin("GET /api/backups/{id}/apply", onBackup(h.apply))
	admin("DELETE /api/backups/{id}", onBackup(func(w http.ResponseWriter, r *http.Request, b Backup) error {
		// A backup whose file cannot be removed stays listed (BackupManager.removeBackup, js:393-401).
		if h.removeFile(b) {
			h.mu.Lock()
			h.backups = slices.DeleteFunc(h.backups, func(x Backup) bool { return x.ID == b.ID })
			h.mu.Unlock()
		}
		return httpx.JSON(w, r, listReply{h.list()})
	}))
}

// listReply is the answer of every route that changes the list.
type listReply struct {
	Backups []Backup `json:"backups"`
}

func (h *Handler) listRoute(w http.ResponseWriter, r *http.Request) error {
	return httpx.JSON(w, r, struct {
		listReply
		BackupLocation   string `json:"backupLocation"`
		BackupPathEnvSet bool   `json:"backupPathEnvSet"`
	}{listReply{h.list()}, h.folder(), h.getenv("BACKUP_PATH") != ""})
}

func (h *Handler) createRoute(w http.ResponseWriter, r *http.Request) error {
	if _, err := h.Create(r.Context()); err != nil {
		return httpx.Internal("backup failed", err)
	}
	return httpx.JSON(w, r, listReply{h.list()})
}

// upload stores a posted archive in the backup folder. The 500 answers and
// their texts are upstream's (BackupController.upload).
func (h *Handler) upload(w http.ResponseWriter, r *http.Request) error {
	invalid := func(err error) error { return httpx.Internal("Invalid backup file", err) }
	moveFailed := func(err error) error { return httpx.Internal("Failed to move backup file into backups directory", err) }
	r.Body = http.MaxBytesReader(w, r.Body, maxArchiveBytes)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return invalid(err)
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		return invalid(err)
	}
	defer file.Close()
	if filepath.Ext(header.Filename) != extension {
		return invalid(errors.New("uploaded file " + header.Filename + " does not end in " + extension))
	}
	dir, name := h.folder(), filepath.Base(header.Filename)
	tmp, err := os.CreateTemp(dir, "upload-*.tmp")
	if err != nil {
		return moveFailed(err)
	}
	defer os.Remove(tmp.Name())
	_, err = io.Copy(tmp, file)
	if err = errors.Join(err, tmp.Close()); err != nil {
		return moveFailed(err)
	}
	b, err := readBackup(tmp.Name())
	if err != nil {
		return httpx.BadRequest(err.Error())
	}
	b.Filename, b.FullPath, b.BackupDirPath, b.Path = name, filepath.Join(dir, name), dir, filepath.Join("backups", name)
	if err = os.Rename(tmp.Name(), b.FullPath); err != nil {
		return moveFailed(err)
	}
	h.put(b, 0)
	return httpx.JSON(w, r, listReply{h.list()})
}

// updatePath moves the backup folder to the posted path (BackupController.updatePath).
func (h *Handler) updatePath(w http.ResponseWriter, r *http.Request) error {
	body, err := httpx.Decode[struct {
		Path string `json:"path"`
	}](w, r)
	if err != nil || strings.TrimSpace(body.Path) == "" {
		return httpx.BadRequest("Invalid request body. Must include path.")
	}
	path, err := filepath.Abs(body.Path)
	if err != nil {
		return httpx.BadRequest("Invalid path")
	}
	if path == h.folder() {
		return httpx.Text(w, http.StatusOK, "Backup path unchanged")
	}
	if err = os.MkdirAll(path, 0o755); err != nil {
		return httpx.BadRequest("Invalid backup path \"" + body.Path + "\"")
	}
	slog.Info("[BackupController] Updating backup path", "to", path, "from", h.folder()) // BackupController.js:101
	if h.getenv("BACKUP_PATH") != "" {
		slog.Warn("[BackupController] Backup path is set in environment variable BACKUP_PATH. Backup path will be reverted on server restart.") // BackupController.js:105
	}
	server, err := h.settings.UpdateServer(r.Context(), func(s *settings.Server) error { s.BackupPath = path; return nil })
	if err != nil {
		return httpx.Internal("failed to save backup path", err)
	}
	// The environment overrides the stored path in the settings; this route
	// moves the running server regardless (until the next start).
	if server.BackupPath != path {
		server.BackupPath = path
		if err = h.configure(server); err != nil {
			return httpx.Internal("failed to reload backup path", err)
		}
	}
	return httpx.JSON(w, r, "OK")
}
