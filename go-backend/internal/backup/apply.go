package backup

import (
	"archive/zip"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"audiobookshelf-compat/internal/httpx"
)

// apply stages the archive in a folder next to the database and hands it to
// the restore function (BackupManager.js:201). Symbolic links, paths that leave
// the archive and more than maxArchiveBytes of content are refused with 400.
func (h *Handler) apply(w http.ResponseWriter, r *http.Request, b Backup) error {
	slog.Info("[BackupManager] Applying backup", "path", b.FullPath) // BackupManager.js:201
	zr, err := zip.OpenReader(b.FullPath)
	if err != nil {
		return httpx.Internal("Invalid backup file", err)
	}
	defer zr.Close()
	var total uint64
	for _, f := range zr.File {
		switch {
		case f.Mode()&os.ModeSymlink != 0:
			return httpx.BadRequest("Backup contains a symbolic link")
		case !fs.ValidPath(strings.TrimSuffix(f.Name, "/")):
			return httpx.BadRequest("Backup contains an invalid path")
		}
		if total += f.UncompressedSize64; total > maxArchiveBytes {
			return httpx.BadRequest("Backup contents exceed extraction limit")
		}
	}
	stage, err := os.MkdirTemp(h.cfg.ConfigPath, "backup-restore-")
	if err != nil {
		return httpx.Internal("Failed to prepare restore", err)
	}
	defer os.RemoveAll(stage)
	items, authors := filepath.Join(stage, "metadata-items"), filepath.Join(stage, "metadata-authors")
	for _, dir := range []string{items, authors} {
		// A missing folder restores as an empty one. The names are valid paths
		// and the files are never links, so nothing lands outside stage.
		if _, err = fs.Stat(zr, filepath.Base(dir)); err != nil {
			err = os.Mkdir(dir, 0o755)
		} else if sub, subErr := fs.Sub(zr, filepath.Base(dir)); subErr != nil {
			err = subErr
		} else {
			err = os.CopyFS(dir, sub)
		}
		if err != nil {
			return httpx.Internal("Failed to extract backup metadata", err)
		}
	}
	in, err := zr.Open(dbEntry)
	if err != nil {
		return httpx.Internal("Invalid backup file. Does not include absdatabase.sqlite. This might be from an older Audiobookshelf server.", err)
	}
	defer in.Close()
	dbTemp := filepath.Join(h.cfg.ConfigPath, "absdatabase-temp.sqlite")
	defer os.Remove(dbTemp)
	if err = copyTo(dbTemp, in); err != nil {
		return httpx.Internal("Failed to extract sqlite db from backup", err)
	}
	if err = h.restore(r.Context(), dbTemp, items, authors); err != nil {
		return httpx.Internal("Failed to apply backup", err)
	}
	if err = httpx.Status(w, http.StatusOK); err != nil {
		return err
	}
	h.emit.ToAll("backup_applied", nil) // BackupManager.js:263-266: sendStatus(200), then the event
	return nil
}

// copyTo writes src to a new private file at path.
func copyTo(path string, src io.Reader) error {
	out, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, src)
	return errors.Join(err, out.Sync(), out.Close())
}
