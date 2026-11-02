package scanner

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// The library watcher follows server/Watcher.js: filesystem events are queued
// per file, a file that is still being written is polled until its mtime stops
// changing, and once no event arrived for the pending delay the queued paths
// are handed to scanFilesChanged, which rescans only the affected library
// items under a "watcher-scan" task.
var (
	watcherPendingDelay = 10 * time.Second // Watcher.js:20
	watcherFilePoll     = 3 * time.Second  // Watcher.js:259
)

const watcherMaxPolls = 200 // Watcher.js:233 (600s safety)

type fileUpdate struct {
	path, relPath, folderID, libraryID, kind string
}

type watchedFolder struct{ id, path string }

type libraryWatch struct {
	id, name string
	folders  []watchedFolder
}

type fsWatcher struct {
	s         *Scanner
	ctx       context.Context
	mu        sync.Mutex
	libraries map[string]*libraryWatch
	pending   []fileUpdate
	being     map[string]bool
	timer     *time.Timer
	task      *watcherTask

	scanMu   sync.Mutex
	scanning bool
	queue    []queuedUpdates
}

type queuedUpdates struct {
	updates []fileUpdate
	task    *watcherTask
}

func newFSWatcher(ctx context.Context, s *Scanner) *fsWatcher {
	return &fsWatcher{s: s, ctx: ctx, libraries: map[string]*libraryWatch{}, being: map[string]bool{}}
}

func (w *fsWatcher) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.timer != nil {
		w.timer.Stop()
	}
}

// onFile is Watcher.onFileAdded / onFileRemoved (Watcher.js:162-190).
func (w *fsWatcher) onFile(libraryID, p, kind string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.addUpdate(libraryID, p, kind) {
		return
	}
	if kind == "added" && !w.being[p] {
		w.being[p] = true
		go w.waitForFileToAdd(p, 0, 0)
	}
}

// waitForFileToAdd polls mtime every few seconds until it stops changing
// (Watcher.js:228-262).
func (w *fsWatcher) waitForFileToAdd(p string, last int64, loop int) {
	for ; ; loop++ {
		if loop >= watcherMaxPolls {
			w.mu.Lock()
			kept := w.pending[:0]
			for _, u := range w.pending {
				if u.path != p {
					kept = append(kept, u)
				}
			}
			w.pending = kept
			delete(w.being, p)
			w.mu.Unlock()
			return
		}
		var mtime int64
		if info, err := os.Stat(p); err == nil {
			mtime = info.ModTime().UnixMilli()
		}
		if mtime == last {
			w.mu.Lock()
			delete(w.being, p)
			w.mu.Unlock()
			return
		}
		select {
		case <-w.ctx.Done():
			return
		case <-time.After(watcherFilePoll):
		}
		last = mtime
	}
}

// addUpdate is Watcher.addFileUpdate (Watcher.js:271-322); the caller holds mu.
func (w *fsWatcher) addUpdate(libraryID, p, kind string) bool {
	for _, u := range w.pending {
		if u.path == p {
			return false
		}
	}
	lib := w.libraries[libraryID]
	if lib == nil {
		return false
	}
	var folder *watchedFolder
	for i := range lib.folders {
		root := filepath.ToSlash(lib.folders[i].path)
		if p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/") {
			folder = &lib.folders[i]
			break
		}
	}
	if folder == nil {
		return false
	}
	relPath := strings.Replace(p, filepath.ToSlash(folder.path), "", 1)
	if shouldIgnore(strings.TrimPrefix(relPath, "/")) {
		return false
	}
	if w.task == nil {
		w.task = newWatcherTask(lib)
		w.s.emitTask("task_started", w.task)
	}
	w.pending = append(w.pending, fileUpdate{path: p, relPath: relPath, folderID: folder.id, libraryID: libraryID, kind: kind})
	w.armTimer()
	return true
}

// watcherTask is Task.toJSON() for the scan tasks (Task.js:53-71).
type watcherTask struct {
	ID              string          `json:"id"`
	Action          string          `json:"action"`
	Data            watcherTaskData `json:"data"`
	Title           string          `json:"title"`
	TitleKey        string          `json:"titleKey"`
	TitleSubs       []string        `json:"titleSubs"`
	Description     *string         `json:"description"`
	DescriptionKey  *string         `json:"descriptionKey"`
	DescriptionSubs []string        `json:"descriptionSubs"`
	Error           *string         `json:"error"`
	ErrorKey        *string         `json:"errorKey"`
	ErrorSubs       []string        `json:"errorSubs"`
	ShowSuccess     bool            `json:"showSuccess"`
	IsFailed        bool            `json:"isFailed"`
	IsFinished      bool            `json:"isFinished"`
	StartedAt       int64           `json:"startedAt"`
	FinishedAt      *int64          `json:"finishedAt"`
}

type watcherTaskData struct {
	LibraryID        string       `json:"libraryId"`
	LibraryName      string       `json:"libraryName"`
	LibraryMediaType string       `json:"libraryMediaType,omitempty"`
	ScanResults      *scanResults `json:"scanResults,omitempty"`
}

// scanResults is the task's scanResults: library scans report "missing",
// watcher scans report "removed" (LibraryScan.js:70-77, LibraryScanner.js:439).
type scanResults struct {
	Added   int    `json:"added"`
	Updated int    `json:"updated"`
	Missing *int   `json:"missing,omitempty"`
	Removed *int   `json:"removed,omitempty"`
	Text    string `json:"text"`
	Elapsed int64  `json:"elapsed"`
}

func newWatcherTask(lib *libraryWatch) *watcherTask {
	return &watcherTask{
		ID: uuid.NewString(), Action: "watcher-scan", Data: watcherTaskData{LibraryID: lib.id, LibraryName: lib.name},
		Title: fmt.Sprintf("Scanning file changes in %q", lib.name), TitleKey: "MessageTaskScanningFileChanges", TitleSubs: []string{lib.name},
		ShowSuccess: true, StartedAt: time.Now().UnixMilli(),
	}
}

// armTimer is Watcher.handlePendingFileUpdatesTimeout (Watcher.js:327-350).
func (w *fsWatcher) armTimer() {
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = time.AfterFunc(watcherPendingDelay, w.fire)
}

func (w *fsWatcher) fire() {
	w.mu.Lock()
	for _, u := range w.pending {
		if w.being[u.path] {
			w.armTimer()
			w.mu.Unlock()
			return
		}
	}
	updates, task := w.pending, w.task
	w.pending, w.task = nil, nil
	w.being = map[string]bool{}
	w.mu.Unlock()
	if len(updates) == 0 {
		if task != nil {
			description, key := "No files to scan", "MessageTaskNoFilesToScan"
			task.Description, task.DescriptionKey = &description, &key
			finishTask(task)
			w.s.emitTask("task_finished", task)
		}
		return
	}
	w.s.scanFilesChanged(w.ctx, updates, task, w)
}

func finishTask(task *watcherTask) {
	finished := time.Now().UnixMilli()
	task.IsFinished, task.FinishedAt = true, &finished
}

// emitTask sends a snapshot so later mutations of the task do not reach
// listeners that hold the payload.
func (s *Scanner) emitTask(event string, task *watcherTask) {
	if s.emit == nil {
		return
	}
	snapshot := *task
	if task.Data.ScanResults != nil {
		results := *task.Data.ScanResults
		snapshot.Data.ScanResults = &results
	}
	s.emit("*", event, snapshot)
}

// scanFilesChanged is LibraryScanner.scanFilesChanged (LibraryScanner.js:
// 372-452): one watcher batch at a time, later batches queue.
func (s *Scanner) scanFilesChanged(ctx context.Context, updates []fileUpdate, task *watcherTask, w *fsWatcher) {
	w.scanMu.Lock()
	if w.scanning {
		w.queue = append(w.queue, queuedUpdates{updates, task})
		w.scanMu.Unlock()
		return
	}
	w.scanning = true
	w.scanMu.Unlock()

	var added, updated, removed int
	order, groups := groupUpdatesByFolder(updates)
	for _, folderID := range order {
		group := groups[folderID]
		results, err := s.scanFolderUpdates(ctx, group[0].libraryID, folderID, group)
		if err != nil {
			continue
		}
		changed := false
		for _, r := range results {
			switch r {
			case scanAdded:
				added++
				changed = true
			case scanUpdated:
				updated++
				changed = true
			case scanRemoved:
				removed++
				changed = true
			}
		}
		if changed {
			s.resetIssuesFilterData(ctx, group[0].libraryID) // LibraryScanner.js:419-441
		}
	}
	var parts []string
	if added > 0 {
		parts = append(parts, fmt.Sprintf("%d added", added))
	}
	if updated > 0 {
		parts = append(parts, fmt.Sprintf("%d updated", updated))
	}
	if removed > 0 {
		parts = append(parts, fmt.Sprintf("%d missing", removed))
	}
	text := "No changes needed"
	if len(parts) > 0 {
		text = strings.Join(parts, ", ")
	}
	task.Data.ScanResults = &scanResults{Added: added, Updated: updated, Removed: &removed, Text: text, Elapsed: time.Now().UnixMilli() - task.StartedAt}
	finishTask(task)
	s.emitTask("task_finished", task)

	w.scanMu.Lock()
	w.scanning = false
	var next *queuedUpdates
	if len(w.queue) > 0 {
		next = &w.queue[0]
		w.queue = w.queue[1:]
	}
	w.scanMu.Unlock()
	if next != nil {
		s.scanFilesChanged(ctx, next.updates, next.task, w)
	}
}

func groupUpdatesByFolder(updates []fileUpdate) ([]string, map[string][]fileUpdate) {
	var order []string
	groups := map[string][]fileUpdate{}
	for _, u := range updates {
		if _, ok := groups[u.folderID]; !ok {
			order = append(order, u.folderID)
		}
		groups[u.folderID] = append(groups[u.folderID], u)
	}
	return order, groups
}

const (
	scanNothing = iota
	scanAdded
	scanUpdated
	scanRemoved
)

// updateGroup keeps the insertion order JavaScript object keys have.
type updateGroup struct {
	keys  []string
	files map[string][]string
}

func (g *updateGroup) add(key, rel string, single bool) {
	if _, ok := g.files[key]; !ok {
		g.keys = append(g.keys, key)
	}
	if single {
		g.files[key] = nil
		return
	}
	g.files[key] = append(g.files[key], rel)
}

func (g *updateGroup) isSingle(key string) bool {
	files, ok := g.files[key]
	return ok && files == nil
}

func (g *updateGroup) drop(key string) {
	delete(g.files, key)
	for i, k := range g.keys {
		if k == key {
			g.keys = append(g.keys[:i:i], g.keys[i+1:]...)
			break
		}
	}
}

func isMediaFileExt(mediaType, ext string, audiobooksOnly bool) bool {
	if ext == "" {
		return false
	}
	if mediaType == "podcast" || audiobooksOnly {
		return audioExt(ext)
	}
	return audioExt(ext) || ebookExt(ext)
}

func isScannableNonMedia(ext string) bool {
	switch strings.ToLower(ext) {
	case ".txt", ".nfo", ".opf", ".abs", ".xml", ".json", ".png", ".jpg", ".jpeg", ".webp":
		return true
	}
	return false
}

// groupFileUpdates is scandir.groupFileItemsIntoLibraryItemDirs with
// includeNonMediaFiles=true (scandir.js:37-110).
func groupFileUpdates(mediaType string, relPaths []string, audiobooksOnly bool) *updateGroup {
	out := &updateGroup{files: map[string][]string{}}
	var media, other []string
	for _, rel := range relPaths {
		rel = strings.TrimPrefix(rel, "/")
		deep := strings.Count(rel, "/")
		ext := filepath.Ext(rel)
		if !(deep > 0 || (mediaType == "book" && isMediaFileExt(mediaType, ext, audiobooksOnly))) {
			continue
		}
		if isMediaFileExt(mediaType, ext, audiobooksOnly) || isScannableNonMedia(ext) {
			media = append(media, rel)
		} else {
			other = append(other, rel)
		}
	}
	for _, rel := range media {
		dirparts := splitNonEmpty(filepath.ToSlash(filepath.Dir(rel)))
		name := filepath.Base(rel)
		if len(dirparts) == 0 {
			out.add(name, name, true)
			continue
		}
		cur := ""
		for i := 0; i < len(dirparts); i++ {
			cur = joinSlash(cur, dirparts[i])
			rest := dirparts[i+1:]
			if _, ok := out.files[cur]; ok {
				out.add(cur, joinSlash(strings.Join(rest, "/"), name), false)
				break
			} else if len(rest) == 0 {
				out.add(cur, name, false)
				break
			} else if len(rest) == 1 && cdDirectory.MatchString(rest[0]) {
				out.add(cur, joinSlash(rest[0], name), false)
				break
			}
		}
	}
	for _, rel := range other {
		dirparts := strings.Split(filepath.ToSlash(filepath.Dir(rel)), "/")
		name := filepath.Base(rel)
		cur := ""
		for i := 0; i < len(dirparts); i++ {
			cur = joinSlash(cur, dirparts[i])
			if _, ok := out.files[cur]; ok {
				out.add(cur, joinSlash(strings.Join(dirparts[i+1:], "/"), name), false)
				break
			}
		}
	}
	return out
}

func splitNonEmpty(s string) []string {
	if s == "." || s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, "/") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func joinSlash(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "/" + b
}

// storedItem is a library item row as the watcher lookups need it.
type storedItem struct {
	id, path, relPath, ino string
	isFile                 bool
}

func (s *Scanner) findStoredItem(ctx context.Context, where string, args ...any) (storedItem, bool) {
	var it storedItem
	var isFile int
	err := s.db.QueryRowContext(ctx, `SELECT id,path,relPath,COALESCE(ino,''),isFile FROM libraryItems WHERE `+where+` ORDER BY rowid LIMIT 1`, args...).Scan(&it.id, &it.path, &it.relPath, &it.ino, &isFile)
	it.isFile = isFile != 0
	return it, err == nil
}

func fileIno(p string) string {
	if info, err := os.Stat(p); err == nil {
		return inode(info)
	}
	return ""
}

// scanFolderUpdates is LibraryScanner.scanFolderUpdates (LibraryScanner.js:
// 493-624) for one library folder.
func (s *Scanner) scanFolderUpdates(ctx context.Context, libraryID, folderID string, updates []fileUpdate) (map[string]int, error) {
	l, err := s.library(ctx, libraryID)
	if err != nil {
		return nil, err
	}
	s.ensureFilterData(ctx, libraryID, l.mediaType)
	var f folder
	if err := s.db.QueryRowContext(ctx, `SELECT id,path FROM libraryFolders WHERE id=? AND libraryId=?`, folderID, libraryID).Scan(&f.id, &f.path); err != nil {
		return nil, err
	}
	f.path = filepath.ToSlash(f.path)
	rels := make([]string, len(updates))
	for i, u := range updates {
		rels[i] = u.relPath
	}
	group := groupFileUpdates(l.mediaType, rels, l.audiobooksOnly)
	results := map[string]int{}
	if len(group.keys) == 0 {
		return results, nil
	}
	tally := &scanTally{}

	// First pass: files moved out of an item folder into its parent
	// (LibraryScanner.js:501-545).
	for _, itemDir := range append([]string(nil), group.keys...) {
		if group.isSingle(itemDir) {
			continue
		}
		var nested []string
		for _, rel := range group.files[itemDir] {
			if strings.Contains(rel, "/") {
				nested = append(nested, rel)
			}
		}
		if len(nested) == 0 {
			continue
		}
		altDir := itemDir + "/" + strings.SplitN(nested[0], "/", 2)[0]
		fullPath := joinSlash(f.path, itemDir)
		if _, ok := s.findStoredItem(ctx, `path LIKE ? ESCAPE '\'`, likePrefix(fullPath+"/")); !ok {
			continue
		}
		if _, ok := s.findStoredItem(ctx, `path LIKE ? ESCAPE '\'`, likePrefix(joinSlash(f.path, altDir)+"/")); ok {
			continue
		}
		group.drop(itemDir)
		var remapped []string
		for _, rel := range nested {
			parts := strings.Split(rel, "/")
			remapped = append(remapped, strings.Join(parts[1:], "/"))
		}
		group.keys = append(group.keys, altDir)
		group.files[altDir] = remapped
	}

	// Second pass: new, updated or removed items.
	for _, itemDir := range group.keys {
		fullPath := joinSlash(f.path, itemDir)
		isSingle := group.isSingle(itemDir)
		candidates := []string{fullPath}
		parts := strings.Split(itemDir, "/")
		for i := 0; i < len(parts)-1; i++ {
			candidates = append(candidates, joinSlash(f.path, strings.Join(parts[:len(parts)-1-i], "/")))
		}
		marks := strings.TrimSuffix(strings.Repeat("?,", len(candidates)), ",")
		args := []any{libraryID}
		for _, c := range candidates {
			args = append(args, c)
		}
		existing, found := s.findStoredItem(ctx, `libraryId=? AND path IN (`+marks+`)`, args...)
		var details *itemPathUpdate
		if !found {
			if ino := fileIno(fullPath); ino != "" {
				existing, found = s.findStoredItem(ctx, `libraryId=? AND ino=?`, libraryID, ino)
				if !found && isSingle {
					existing, found = s.findStoredItem(ctx, `libraryId=? AND EXISTS(SELECT 1 FROM json_each(libraryFiles) WHERE json_valid(json_each.value) AND json_each.value->>'$.ino' = ?)`, libraryID, ino)
				}
			}
			if !found && !isSingle {
				var inos []any
				for _, rel := range group.files[itemDir] {
					if ino := fileIno(joinSlash(fullPath, rel)); ino != "" {
						inos = append(inos, ino)
					}
				}
				if len(inos) > 0 {
					existing, found = s.findStoredItem(ctx, `libraryId=? AND ino IN (`+strings.TrimSuffix(strings.Repeat("?,", len(inos)), ",")+`)`, append([]any{libraryID}, inos...)...)
				}
			}
			if found {
				details = &itemPathUpdate{path: fullPath, folderID: f.id, isFile: isSingle}
			}
		}
		if found {
			if existing.path == fullPath {
				if _, statErr := os.Stat(fullPath); statErr != nil {
					now := nowDB()
					if _, e := s.db.ExecContext(ctx, `UPDATE libraryItems SET isMissing=1,updatedAt=? WHERE id=?`, now, existing.id); e != nil {
						return results, e
					}
					if item, e := s.items.ItemJSON(ctx, existing.id, true); e == nil && s.emit != nil {
						s.emit("*", "item_updated", item)
					}
					results[itemDir] = scanRemoved
					continue
				}
			}
			res, e := s.rescanStoredItem(ctx, existing.id, details, tally)
			if e != nil {
				return results, e
			}
			results[itemDir] = res
			continue
		}
		if l.audiobooksOnly && !groupHasAudio(group, itemDir) {
			continue
		}
		if _, statErr := os.Stat(fullPath); statErr != nil {
			results[itemDir] = scanNothing
			continue
		}
		if _, child := s.findStoredItem(ctx, `path LIKE ? ESCAPE '\'`, likePrefix(fullPath+"/")); child {
			results[itemDir] = scanNothing
			continue
		}
		g, err := s.collectItemGroup(ctx, fullPath, f, isSingle)
		if err != nil || g == nil {
			results[itemDir] = scanNothing
			continue
		}
		g.brandNew = true
		n, e := s.importGroup(ctx, l, g, false)
		if e != nil {
			return results, e
		}
		tally.merge(n)
		if n.added {
			if item, e := s.items.ItemJSON(ctx, n.id, true); e == nil && s.emit != nil {
				s.emit("*", "item_added", item)
			}
			results[itemDir] = scanAdded
		} else {
			results[itemDir] = scanNothing
		}
		// Watcher path skips the full-scan cleanup: only the author book counts
		// are announced (LibraryItemScanner.js:215-217).
		_ = s.emitAuthorCounts(ctx, libraryID, tally)
		tally.authorsChanged = nil
	}
	return results, nil
}

func groupHasAudio(g *updateGroup, itemDir string) bool {
	if g.isSingle(itemDir) {
		return audioExt(filepath.Ext(itemDir))
	}
	for _, rel := range g.files[itemDir] {
		if audioExt(filepath.Ext(rel)) {
			return true
		}
	}
	return false
}

func likePrefix(prefix string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(prefix) + "%"
}

type itemPathUpdate struct {
	path, folderID string
	isFile         bool
}

// rescanStoredItem is LibraryItemScanner.scanLibraryItem (LibraryItemScanner.js:
// 24-71) for the watcher: rescan one stored item, optionally at a new path.
func (s *Scanner) rescanStoredItem(ctx context.Context, itemID string, details *itemPathUpdate, tally *scanTally) (int, error) {
	var libraryID, itemPath, folderID string
	var isFile int
	if err := s.db.QueryRowContext(ctx, `SELECT libraryId,path,libraryFolderId,isFile FROM libraryItems WHERE id=?`, itemID).Scan(&libraryID, &itemPath, &folderID, &isFile); err != nil {
		return scanNothing, err
	}
	single := isFile != 0
	if details != nil {
		itemPath, folderID, single = details.path, details.folderID, details.isFile
	}
	var f folder
	if err := s.db.QueryRowContext(ctx, `SELECT id,path FROM libraryFolders WHERE id=?`, folderID).Scan(&f.id, &f.path); err != nil {
		return scanNothing, err
	}
	l, err := s.library(ctx, libraryID)
	if err != nil {
		return scanNothing, err
	}
	g, err := s.collectItemGroup(ctx, itemPath, f, single)
	if err != nil {
		return scanNothing, err
	}
	if g == nil {
		return scanNothing, nil
	}
	g.existingID = itemID
	n, err := s.importGroup(ctx, l, g, true)
	if err != nil {
		return scanNothing, err
	}
	if !n.updated {
		return scanNothing, nil
	}
	tally.merge(n)
	if item, e := s.rescanItemJSON(ctx, n.id, n.scannedAudio); e == nil && s.emit != nil {
		s.emit("*", "item_updated", item)
	}
	if err := s.finalizeScan(ctx, libraryID, &scanTally{authorsChanged: n.authorsChanged, authorsRemoved: n.authorsRemoved, seriesRemoved: n.seriesRemoved}); err != nil {
		return scanUpdated, err
	}
	return scanUpdated, nil
}

func (s *Scanner) emitAuthorCounts(ctx context.Context, libraryID string, t *scanTally) error {
	only := &scanTally{authorsChanged: t.authorsChanged}
	return s.finalizeScan(ctx, libraryID, only)
}

// collectItemGroup lists an item folder's files into a scan group.
func (s *Scanner) collectItemGroup(ctx context.Context, itemPath string, f folder, isFile bool) (*group, error) {
	st, err := os.Stat(itemPath)
	if err != nil {
		return nil, err
	}
	var files []file
	if st.IsDir() && !isFile {
		err = filepath.WalkDir(itemPath, func(p string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if d.IsDir() {
				if p != itemPath && (strings.HasPrefix(d.Name(), ".") || d.Name() == "@eaDir") {
					return filepath.SkipDir
				}
				return nil
			}
			if shouldIgnore(mustRel(itemPath, p)) || (!audioExt(filepath.Ext(p)) && !scannableCompanion(filepath.Ext(p))) {
				return nil
			}
			info, e := d.Info()
			if e != nil {
				return e
			}
			files = append(files, file{path: p, info: info, relPath: filepath.ToSlash(mustRel(itemPath, p))})
			return nil
		})
	} else if audioExt(filepath.Ext(itemPath)) || ebookExt(filepath.Ext(itemPath)) {
		files = append(files, file{path: itemPath, info: st, relPath: filepath.Base(itemPath)})
	}
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, nil
	}
	g := &group{path: itemPath, folder: f, isFile: isFile}
	for _, fl := range files {
		if audioExt(filepath.Ext(fl.path)) || (isFile && ebookExt(filepath.Ext(fl.path))) {
			g.files = append(g.files, fl)
		} else {
			g.companions = append(g.companions, fl)
		}
	}
	return g, nil
}

var _ = sql.ErrNoRows
