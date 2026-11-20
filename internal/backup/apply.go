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

	"monstrobook/internal/httpx"
)

var errArchiveLimit = errors.New("backup contents exceed extraction limit")

type byteBudget struct {
	remaining int64
}

func (b *byteBudget) take(n int64) error {
	b.remaining -= n
	if b.remaining < 0 {
		return errArchiveLimit
	}
	return nil
}

type limitedFS struct {
	fs.FS
	budget *byteBudget
}

func (l limitedFS) Open(name string) (fs.File, error) {
	f, err := l.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return &limitedFile{File: f, budget: l.budget}, nil
}

func (l limitedFS) Stat(name string) (fs.FileInfo, error) {
	return fs.Stat(l.FS, name)
}

func (l limitedFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return fs.ReadDir(l.FS, name)
}

type limitedFile struct {
	fs.File
	budget *byteBudget
}

func (f *limitedFile) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if f.budget.remaining <= 0 {
		p = p[:1]
	} else if int64(len(p)) > f.budget.remaining+1 {
		p = p[:f.budget.remaining+1]
	}
	n, err := f.File.Read(p)
	if takeErr := f.budget.take(int64(n)); takeErr != nil {
		return 0, takeErr
	}
	return n, err
}

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
	limit := h.maxExtractBytes
	if limit <= 0 {
		limit = maxArchiveBytes
	}
	limitU := uint64(limit)
	var total uint64
	for _, f := range zr.File {
		switch {
		case f.Mode()&os.ModeSymlink != 0:
			return httpx.BadRequest("Backup contains a symbolic link")
		case !fs.ValidPath(strings.TrimSuffix(f.Name, "/")):
			return httpx.BadRequest("Backup contains an invalid path")
		}
		if f.UncompressedSize64 > limitU || total > limitU-f.UncompressedSize64 {
			return httpx.BadRequest("Backup contents exceed extraction limit")
		}
		total += f.UncompressedSize64
	}
	stage, err := os.MkdirTemp(h.cfg.ConfigPath, "backup-restore-")
	if err != nil {
		return httpx.Internal("Failed to prepare restore", err)
	}
	defer os.RemoveAll(stage)
	budget := &byteBudget{remaining: limit}
	items, authors := filepath.Join(stage, "metadata-items"), filepath.Join(stage, "metadata-authors")
	for _, dir := range []string{items, authors} {
		// A missing folder restores as an empty one. The names are valid paths
		// and the files are never links, so nothing lands outside stage.
		if _, err = fs.Stat(zr, filepath.Base(dir)); err != nil {
			err = os.Mkdir(dir, 0o755)
		} else if sub, subErr := fs.Sub(zr, filepath.Base(dir)); subErr != nil {
			err = subErr
		} else {
			err = os.CopyFS(dir, limitedFS{FS: sub, budget: budget})
		}
		if err != nil {
			if errors.Is(err, errArchiveLimit) {
				return httpx.BadRequest("Backup contents exceed extraction limit")
			}
			return httpx.Internal("Failed to extract backup metadata", err)
		}
	}
	in, err := zr.Open(dbEntry)
	if err != nil {
		return httpx.Internal("Invalid backup file. Does not include absdatabase.sqlite. This might be from an older Audiobookshelf server.", err)
	}
	defer in.Close()
	dbFile, err := os.CreateTemp(h.cfg.ConfigPath, "absdatabase-*.sqlite")
	if err != nil {
		return httpx.Internal("Failed to extract sqlite db from backup", err)
	}
	dbTemp := dbFile.Name()
	defer os.Remove(dbTemp)
	if err = copyToFile(dbFile, io.LimitReader(in, budget.remaining+1)); err != nil {
		return httpx.Internal("Failed to extract sqlite db from backup", err)
	}
	info, err := os.Stat(dbTemp)
	if err != nil {
		return httpx.Internal("Failed to extract sqlite db from backup", err)
	}
	if err := budget.take(info.Size()); err != nil {
		return httpx.BadRequest("Backup contents exceed extraction limit")
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

// copyToFile writes src to out, syncs and closes it.
func copyToFile(out *os.File, src io.Reader) error {
	_, err := io.Copy(out, src)
	return errors.Join(err, out.Sync(), out.Close())
}
