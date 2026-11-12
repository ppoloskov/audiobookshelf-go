package backup

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	extension       = ".audiobookshelf"
	dbEntry         = "absdatabase.sqlite"
	maxArchiveBytes = 20 << 30 // restore and upload cap
	prettyDate      = "Mon, Jan 2 2006 15:04"
)

// Backup is one archive, in the JSON shape of upstream's Backup.toJSON().
type Backup struct {
	ID            string  `json:"id"`
	Key           *string `json:"key"` // "sqlite"; null for the legacy value 1
	BackupDirPath string  `json:"backupDirPath"`
	DatePretty    string  `json:"datePretty"`
	FullPath      string  `json:"fullPath"`
	Path          string  `json:"path"`
	Filename      string  `json:"filename"`
	FileSize      int64   `json:"fileSize"`
	CreatedAt     int64   `json:"createdAt"`
	ServerVersion string  `json:"serverVersion"`
}

// writeArchive creates the zip of b: the database snapshot, the item and author
// metadata folders and the details file upstream reads back. It returns the
// uncompressed bytes of the files it stored (details excluded).
func writeArchive(b Backup, snapshot, itemsDir, authorsDir string) (written int64, err error) {
	out, err := os.Create(b.FullPath)
	if err != nil {
		return 0, err
	}
	zw := zip.NewWriter(out)
	add := func(path, name string) error {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		n, err := io.Copy(w, f)
		written += n
		return err
	}
	addTree := func(root, name string) error {
		if _, err := os.Stat(root); errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fs.WalkDir(os.DirFS(root), ".", func(rel string, d fs.DirEntry, err error) error {
			switch {
			case err != nil || rel == ".":
				return err
			case d.IsDir():
				_, err = zw.Create(name + "/" + rel + "/")
				return err
			}
			return add(filepath.Join(root, rel), name+"/"+rel)
		})
	}
	err = add(snapshot, dbEntry)
	if err == nil {
		err = addTree(itemsDir, "metadata-items")
	}
	if err == nil {
		err = addTree(authorsDir, "metadata-authors")
	}
	if err == nil {
		var w io.Writer
		if w, err = zw.Create("details"); err == nil {
			key := ""
			if b.Key != nil {
				key = *b.Key
			}
			_, err = fmt.Fprintf(w, "%s\n%s\n%d\n%s", b.ID, key, b.CreatedAt, b.ServerVersion)
		}
	}
	return written, errors.Join(err, zw.Close(), out.Close())
}

// readBackup reads the details of an archive. The messages are upstream's
// (BackupManager.js:296-312); the upload route sends them to the client.
func readBackup(path string) (Backup, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return Backup{}, errors.New("Failed to read backup file - backup might not be a valid .zip file")
	}
	defer zr.Close()
	if _, err := fs.Stat(zr, dbEntry); err != nil {
		return Backup{}, errors.New("Invalid backup with no absdatabase.sqlite file - might be a backup created on an old Audiobookshelf server.")
	}
	details, err := fs.Stat(zr, "details")
	if err != nil {
		return Backup{}, errors.New("Invalid backup file - missing details entry")
	}
	if details.Size() > 1<<20 {
		return Backup{}, errors.New("Invalid backup file - details entry too large")
	}
	raw, err := fs.ReadFile(zr, "details")
	if err != nil {
		return Backup{}, err
	}
	lines := strings.Split(string(raw), "\n")
	if len(lines) < 4 || strings.TrimSpace(lines[3]) == "" {
		return Backup{}, errors.New("Invalid backup. Might be from before version 2.0.0.")
	}
	created, _ := strconv.ParseInt(strings.TrimSpace(lines[2]), 10, 64) // upstream's Number() reads garbage as NaN, shown as 0
	key := ptr(strings.TrimSpace(lines[1]))
	if *key == "1" {
		key = nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return Backup{}, err
	}
	name := filepath.Base(path)
	return Backup{ID: strings.TrimSpace(lines[0]), Key: key, BackupDirPath: filepath.Dir(path), DatePretty: time.UnixMilli(created).Format(prettyDate),
		FullPath: path, Path: filepath.Join("backups", name), Filename: name, FileSize: info.Size(), CreatedAt: created, ServerVersion: strings.TrimSpace(lines[3])}, nil
}
