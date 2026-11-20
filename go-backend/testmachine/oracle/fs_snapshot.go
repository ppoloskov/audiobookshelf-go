package oracle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maxSnapshotJSONBytes = 16 << 20

// FileEntry captures metadata, content digest, and optional decoded JSON for a file.
type FileEntry struct {
	Rel    string
	Size   int64
	SHA256 string
	JSON   any
}

// FSSnapshot holds regular file entries under metadata/ and media/.
type FSSnapshot struct {
	Files map[string]FileEntry
}

// SnapshotFS walks metadata/ and media/ under installDir, skipping volatile
// metadata/logs and metadata/cache directories, hashing files, and decoding *.json.
func SnapshotFS(installDir string) (*FSSnapshot, error) {
	info, err := os.Stat(installDir)
	if err != nil {
		return nil, fmt.Errorf("stat install dir %s: %w", installDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("install path %s is not a directory", installDir)
	}

	snap := &FSSnapshot{Files: make(map[string]FileEntry)}
	for _, sub := range []string{"metadata", "media"} {
		root := filepath.Join(installDir, sub)
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("stat %s: %w", root, err)
		}
		if err := walkSnapshotRoot(installDir, root, snap.Files); err != nil {
			return nil, err
		}
	}
	return snap, nil
}

func walkSnapshotRoot(installDir, root string, dst map[string]FileEntry) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk %s: %w", p, walkErr)
		}
		rel, err := filepath.Rel(installDir, p)
		if err != nil {
			return fmt.Errorf("relative path %s: %w", p, err)
		}
		relSlash := filepath.ToSlash(rel)
		if d.IsDir() {
			if relSlash == "metadata/logs" || relSlash == "metadata/cache" ||
				strings.HasPrefix(relSlash, "metadata/logs/") || strings.HasPrefix(relSlash, "metadata/cache/") {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		entry, err := readFileEntry(p, relSlash)
		if err != nil {
			return err
		}
		dst[relSlash] = entry
		return nil
	})
}

func readFileEntry(absPath, relSlash string) (FileEntry, error) {
	info, err := os.Stat(absPath)
	if err != nil {
		return FileEntry{}, fmt.Errorf("stat %s: %w", absPath, err)
	}
	f, err := os.Open(absPath)
	if err != nil {
		return FileEntry{}, fmt.Errorf("open %s: %w", absPath, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return FileEntry{}, fmt.Errorf("hash %s: %w", absPath, err)
	}

	entry := FileEntry{
		Rel:    relSlash,
		Size:   info.Size(),
		SHA256: hex.EncodeToString(h.Sum(nil)),
	}
	if strings.EqualFold(filepath.Ext(relSlash), ".json") {
		if info.Size() > maxSnapshotJSONBytes {
			return FileEntry{}, fmt.Errorf("json file %s exceeds %d bytes", absPath, maxSnapshotJSONBytes)
		}
		data, err := os.ReadFile(absPath)
		if err != nil {
			return FileEntry{}, fmt.Errorf("read json %s: %w", absPath, err)
		}
		val, err := decodeJSONBytes(data)
		if err != nil {
			return FileEntry{}, fmt.Errorf("decode json %s: %w", absPath, err)
		}
		entry.JSON = val
	}
	return entry, nil
}

// DiffFS snapshots metadata/ and media/ for both installs and diffs normalized
// relative paths, JSON payloads (such as metadata.json), and file digests.
func (d *Differ) DiffFS(step, tsInstall, goInstall string) ([]Finding, error) {
	tsSnap, err := SnapshotFS(tsInstall)
	if err != nil {
		return nil, fmt.Errorf("snapshot ts fs: %w", err)
	}
	goSnap, err := SnapshotFS(goInstall)
	if err != nil {
		return nil, fmt.Errorf("snapshot go fs: %w", err)
	}

	tsByNorm := make(map[string]FileEntry, len(tsSnap.Files))
	goByNorm := make(map[string]FileEntry, len(goSnap.Files))
	pathSet := make(map[string]struct{}, len(tsSnap.Files)+len(goSnap.Files))
	for rel, entry := range tsSnap.Files {
		norm := d.Corr.NormalizeString("ts", rel)
		tsByNorm[norm] = entry
		pathSet[norm] = struct{}{}
	}
	for rel, entry := range goSnap.Files {
		norm := d.Corr.NormalizeString("go", rel)
		goByNorm[norm] = entry
		pathSet[norm] = struct{}{}
	}

	paths := make([]string, 0, len(pathSet))
	for p := range pathSet {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var findings []Finding
	for _, p := range paths {
		te, okTS := tsByNorm[p]
		ge, okGo := goByNorm[p]
		if !okTS {
			findings = append(findings, Finding{Step: step, Surface: "fs", Path: p, TSValue: "<missing>", GoValue: p, Detail: "extra file in Go"})
			continue
		}
		if !okGo {
			findings = append(findings, Finding{Step: step, Surface: "fs", Path: p, TSValue: p, GoValue: "<missing>", Detail: "missing file in Go"})
			continue
		}
		if te.JSON != nil && ge.JSON != nil {
			findings = append(findings, d.diffJSON(step, "fs", p, te.JSON, ge.JSON)...)
			continue
		}
		if te.SHA256 != ge.SHA256 {
			findings = append(findings, Finding{
				Step:    step,
				Surface: "fs",
				Path:    p,
				TSValue: fmt.Sprintf("size=%d sha256=%s", te.Size, te.SHA256[:16]),
				GoValue: fmt.Sprintf("size=%d sha256=%s", ge.Size, ge.SHA256[:16]),
				Detail:  "filesystem file content mismatch",
			})
		}
	}
	return findings, nil
}
