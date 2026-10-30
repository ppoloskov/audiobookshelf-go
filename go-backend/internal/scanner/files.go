package scanner

import (
	"audiobookshelf-compat/internal/dbtime"
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
)

var audioExtensions = map[string]bool{".m4b": true, ".mp3": true, ".m4a": true, ".flac": true, ".opus": true, ".ogg": true, ".oga": true, ".mp4": true, ".aac": true, ".wma": true, ".aiff": true, ".aif": true, ".wav": true, ".webm": true, ".webma": true, ".mka": true, ".awb": true, ".caf": true, ".mpg": true, ".mpeg": true}

func audioExt(ext string) bool { return audioExtensions[strings.ToLower(ext)] }
func ebookExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".epub", ".pdf", ".mobi", ".azw3", ".cbr", ".cbz":
		return true
	}
	return false
}

type file struct {
	path    string
	info    os.FileInfo
	relPath string
}
type group struct {
	path       string
	folder     folder
	files      []file
	companions []file
	isFile     bool
}

// bookMetadata mirrors the metadata keys supported by the v2.37.2 metadata.json
// generator. Empty values are omitted so a partial sidecar cannot erase tags.
type bookMetadata struct {
	Title, Subtitle, PublishedYear, PublishedDate, Publisher, Description string
	ISBN, ASIN, Language                                                  string
	Authors, Narrators, Tags, Genres                                      []string
	Series                                                                []seriesMetadata
	Chapters                                                              []map[string]any
	absPresent                                                            map[string]bool
}

type seriesMetadata struct {
	Name     string `json:"name"`
	Sequence string `json:"sequence"`
}
type imported struct {
	id             string
	added, updated bool
}

func groupKey(root, p, mediaType string) string {
	rel, _ := filepath.Rel(root, p)
	if mediaType == "podcast" {
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) > 1 {
			return filepath.Join(root, filepath.FromSlash(parts[0]))
		}
	}
	dir := filepath.Dir(p)
	if dir == root {
		return p
	}
	base := filepath.Base(dir)
	if cdDirectory.MatchString(base) {
		parent := filepath.Dir(dir)
		if parent != root {
			return parent
		}
	}
	return dir
}

var cdDirectory = regexp.MustCompile(`(?i)^(cd|dis[ck])\s*\d{1,3}$`)

func shouldIgnore(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for _, part := range parts {
		if strings.HasPrefix(part, ".") || strings.Contains(part, "@eaDir") {
			return true
		}
	}
	if len(parts) > 0 && parts[len(parts)-1] == ".ignore" {
		return true
	}
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".part", ".tmp", ".crdownload", ".download", ".bak", ".old", ".temp", ".tempfile", ".tempfile~":
		return true
	}
	return false
}
func sortedGroups(m map[string]*group) []*group {
	out := make([]*group, 0, len(m))
	for _, g := range m {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out
}

func (s *Scanner) importGroup(ctx context.Context, l library, g *group, force bool) (imported, error) {
	var oldID, oldMediaID, oldPath, oldRel, oldIno, oldMediaType string
	var oldFiles []byte
	err := s.db.QueryRowContext(ctx, `SELECT id,COALESCE(mediaId,''),path,relPath,COALESCE(ino,''),COALESCE(mediaType,''),libraryFiles FROM libraryItems WHERE libraryId=? AND path=?`, l.id, g.path).Scan(&oldID, &oldMediaID, &oldPath, &oldRel, &oldIno, &oldMediaType, &oldFiles)
	// Upstream prefers exact path, then item inode, then item inode against its
	// own library-file inodes. The reciprocal file-to-item-inode fallback only
	// applies to single-file items; this grouped scanner deliberately omits it.
	if err == sql.ErrNoRows {
		rows, queryErr := s.db.QueryContext(ctx, `SELECT id,COALESCE(mediaId,''),path,relPath,COALESCE(ino,''),COALESCE(mediaType,''),libraryFiles FROM libraryItems WHERE libraryId=?`, l.id)
		if queryErr != nil {
			return imported{}, queryErr
		}
		type candidate struct {
			id, mediaID, path, rel, ino, mediaType string
			files                                  []byte
		}
		var candidates []candidate
		for rows.Next() {
			var c candidate
			if queryErr = rows.Scan(&c.id, &c.mediaID, &c.path, &c.rel, &c.ino, &c.mediaType, &c.files); queryErr != nil {
				rows.Close()
				return imported{}, queryErr
			}
			candidates = append(candidates, c)
		}
		if queryErr = rows.Err(); queryErr != nil {
			rows.Close()
			return imported{}, queryErr
		}
		rows.Close()
		var scannedInos []string
		if info, statErr := os.Stat(g.path); statErr == nil {
			scannedInos = append(scannedInos, inode(info))
		}
		for _, f := range g.files {
			scannedInos = append(scannedInos, inode(f.info))
		}
		for _, f := range g.companions {
			scannedInos = append(scannedInos, inode(f.info))
		}
		for _, c := range candidates {
			// Upstream LibraryScanner.js:641-647: match the existing item inode
			// to the scanned item/files, or an existing item's library-file
			// inode to the scanned item/files. Both sides must be compared
			// across the candidate and the scan group; a candidate's own file
			// always matches its own directory inode (single-file items).
			match := c.ino != "" && containsString(scannedInos, c.ino)
			if !match {
				var libraryFiles []map[string]any
				_ = json.Unmarshal(c.files, &libraryFiles)
				for _, lf := range libraryFiles {
					if lfIno, ok := lf["ino"].(string); ok && lfIno != "" && containsString(scannedInos, lfIno) {
						match = true
						break
					}
				}
			}
			if match {
				oldID, oldMediaID, oldPath, oldRel, oldIno, oldMediaType, oldFiles = c.id, c.mediaID, c.path, c.rel, c.ino, c.mediaType, c.files
				err = nil
				break
			}
		}
	}
	added := err == sql.ErrNoRows
	if err != nil && !added {
		return imported{}, err
	}
	itemID := oldID
	if added {
		itemID = uuid.NewString()
	}
	rel, _ := filepath.Rel(g.folder.path, g.path)
	rel = filepath.ToSlash(rel)
	mediaID := oldMediaID
	if added || mediaID == "" {
		mediaID = uuid.NewString()
	}
	relatedFiles := append(append([]file(nil), g.files...), g.companions...)
	filesMeta := make([]map[string]any, 0, len(relatedFiles))
	audio := make([]map[string]any, 0, len(g.files))
	chapters := []map[string]any{}
	duration := 0.0
	size := int64(0)
	var itemIno string
	newest := time.Time{}
	for _, f := range relatedFiles {
		filesMeta = append(filesMeta, makeFileMeta(g.path, f))
		size += f.info.Size()
		if f.info.ModTime().After(newest) {
			newest = f.info.ModTime()
		}
	}
	title, author, series, seq := bookPathMetadata(g.path, g.folder.path, l.parseSubtitle)
	if g.isFile {
		title = strings.TrimSuffix(filepath.Base(g.path), filepath.Ext(g.path))
	}
	metadata := bookMetadata{}
	var firstTags map[string]any
	if len(g.files) > 0 {
		firstTags = audioTagsFromFile(g.files[0].path)
	}
	if l.mediaType != "podcast" {
		order := l.metadataPrecedence
		abs, hasABS := readABSMetadata(g, oldID, s.cfg.MetadataPath)
		metadata = mergeBookMetadataWithABS(title, author, series, seq, g, order, firstTags, abs, hasABS)
	}
	if metadata.Title != "" {
		title = metadata.Title
	}
	if len(metadata.Authors) > 0 {
		author = strings.Join(metadata.Authors, ", ")
	}
	if len(metadata.Series) > 0 {
		series, seq = metadata.Series[0].Name, metadata.Series[0].Sequence
	}
	for _, f := range g.files {
		if ctx.Err() != nil {
			return imported{}, ctx.Err()
		}
		if itemIno == "" {
			itemIno = inode(f.info)
		}
		fileMeta := makeFileMeta(g.path, f)
		flat, _ := fileMeta["metadata"].(map[string]any)
		ino, _ := fileMeta["ino"].(string)
		probeMeta := make(map[string]any, len(flat)+1)
		for key, value := range flat {
			probeMeta[key] = value
		}
		probeMeta["scanMetadata"] = map[string]any{"title": title, "author": author, "series": series, "publishedYear": metadata.PublishedYear}
		af, err := probeAudio(ctx, f.path, probeMeta, ino)
		if err != nil || af == nil {
			continue
		}
		if !added {
			if prior, unchanged := s.priorAudioEntry(oldMediaID, f.path, inode(f.info), ms(f.info.ModTime())); prior != nil {
				af["addedAt"] = prior["addedAt"]
				if unchanged {
					af["updatedAt"] = prior["updatedAt"]
				}
				// AudioFile.updateFromScan retains the established entry identity
				// (AudioFile.js:171-196); scanned data replaces probe fields.
			} else {
				af["addedAt"] = ms(f.info.ModTime())
				af["updatedAt"] = ms(f.info.ModTime())
			}
		} else {
			af["addedAt"] = ms(f.info.ModTime())
			af["updatedAt"] = ms(f.info.ModTime())
		}
		audio = append(audio, af)
	}
	if added && len(audio) == 0 && l.mediaType != "podcast" {
		return imported{}, nil
	}
	if l.mediaType != "podcast" {
		audio = smartAudioOrder(audio)
	}
	for _, af := range audio {
		if d, ok := af["duration"].(float64); ok {
			duration += d
		}
	}
	chapters = buildChapters(audio, title)
	itemInfo, itemStatErr := os.Stat(g.path)
	if itemStatErr == nil {
		itemIno = inode(itemInfo)
	}
	if itemIno == "" {
		return imported{}, nil
	}
	now := nowDB()
	mtime, ctime, birthtime := timeString(newest), timeString(newest), timeString(newest)
	if itemStatErr == nil {
		mt, ct, bt := statTimes(itemInfo)
		mtime, ctime, birthtime = timeString(mt), timeString(ct), timeString(bt)
	}
	if mtime == "" {
		mtime, ctime, birthtime = now, now, now
	}
	authors := append([]string(nil), metadata.Authors...)
	if len(authors) == 0 && author != "" {
		authors = []string{author}
	}
	if len(metadata.Chapters) > 0 {
		chapters = metadata.Chapters
	}
	if added && l.mediaType != "podcast" && len(audio) == 0 {
		return imported{}, nil
	}
	if l.mediaType == "podcast" {
		title = filepath.Base(g.path)
		author = ""
		series = ""
	}
	fj, _ := json.Marshal(filesMeta)
	var tx, txerr = s.db.BeginTx(ctx, nil)
	if txerr != nil {
		return imported{}, txerr
	}
	defer tx.Rollback()
	var addedAuthors, addedSeries []map[string]any
	var affectedAuthors []string
	var existingAffectedAuthors []string
	if l.mediaType == "podcast" {
		if added {
			if _, err = tx.ExecContext(ctx, `INSERT INTO podcasts(id,title,author,releaseDate,feedURL,imageURL,description,itunesPageURL,itunesId,itunesArtistId,language,podcastType,explicit,autoDownloadEpisodes,autoDownloadSchedule,lastEpisodeCheck,maxEpisodesToKeep,maxNewEpisodesToDownload,coverPath,tags,genres,createdAt,updatedAt) VALUES(?,?,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,'episodic',0,0,'0 * * * *',?,0,3,NULL,'[]','[]',?,?)`, mediaID, title, now, now, now); err != nil {
				return imported{}, err
			}
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE podcasts SET title=CASE WHEN title IS NULL OR title='' THEN ? ELSE title END,updatedAt=? WHERE id=?`, title, now, mediaID)
			if err != nil {
				return imported{}, err
			}
		}
		// A podcast directory is the library item; each audio file is an episode.
		var count int
		_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM podcastEpisodes WHERE podcastId=?`, mediaID).Scan(&count)
		for i, af := range audio {
			filename := audioFilename(af)
			var existing string
			_ = tx.QueryRowContext(ctx, `SELECT id FROM podcastEpisodes WHERE podcastId=? AND title=? ORDER BY "index" LIMIT 1`, mediaID, filename).Scan(&existing)
			if existing != "" {
				continue
			}
			episodeTitle := filename
			if tags, ok := af["metadata"].(map[string]any); ok {
				if val, ok := tags["tagTitle"].(string); ok && val != "" {
					episodeTitle = val
				}
			}
			raw, _ := json.Marshal(af)
			eid := uuid.NewString()
			if _, err = tx.ExecContext(ctx, `INSERT INTO podcastEpisodes(id,podcastId,"index",season,episode,episodeType,title,subtitle,description,enclosureURL,enclosureSize,enclosureType,pubDate,audioFile,chapters,extraData,publishedAt,createdAt,updatedAt) VALUES(?,?,?,NULL,?,'full',?,NULL,NULL,NULL,NULL,NULL,NULL,?,'[]','{}',NULL,?,?)`, eid, mediaID, count+i+1, strconv.Itoa(count+i+1), episodeTitle, string(raw), now, now); err != nil {
				return imported{}, err
			}
		}
	} else {
		if added {
			if _, err = tx.ExecContext(ctx, `INSERT INTO books(id,title,titleIgnorePrefix,subtitle,publishedYear,publishedDate,publisher,description,isbn,asin,language,explicit,abridged,coverPath,duration,narrators,audioFiles,ebookFile,chapters,tags,genres,createdAt,updatedAt) VALUES(?,?,?,?,?,?,?,?,?,?,?,0,0,NULL,?,?,?,NULL,?,?,?, ?,?)`, mediaID, title, scannerTitleSortKey(title, l.sortingPrefixes), nullableString(metadata.Subtitle), nullableString(metadata.PublishedYear), nullableString(metadata.PublishedDate), nullableString(metadata.Publisher), nullableString(metadata.Description), nullableString(metadata.ISBN), nullableString(metadata.ASIN), nullableString(metadata.Language), duration, mustJSON(metadata.Narrators), mustJSON(audio), mustJSON(chapters), mustJSON(metadata.Tags), mustJSON(metadata.Genres), now, now); err != nil {
				return imported{}, err
			}
		} else if force || !sameBytes(oldFiles, fj) {
			if _, err = tx.ExecContext(ctx, `UPDATE books SET title=COALESCE(?,title),titleIgnorePrefix=?,subtitle=COALESCE(?,subtitle),publishedYear=COALESCE(?,publishedYear),publishedDate=COALESCE(?,publishedDate),publisher=COALESCE(?,publisher),description=COALESCE(?,description),isbn=COALESCE(?,isbn),asin=COALESCE(?,asin),language=COALESCE(?,language),duration=?,audioFiles=?,chapters=?,tags=CASE WHEN ?='[]' THEN tags ELSE ? END,genres=CASE WHEN ?='[]' THEN genres ELSE ? END,narrators=CASE WHEN ?='[]' THEN narrators ELSE ? END,updatedAt=? WHERE id=?`, nullableString(metadata.Title), scannerTitleSortKey(title, l.sortingPrefixes), nullableString(metadata.Subtitle), nullableString(metadata.PublishedYear), nullableString(metadata.PublishedDate), nullableString(metadata.Publisher), nullableString(metadata.Description), nullableString(metadata.ISBN), nullableString(metadata.ASIN), nullableString(metadata.Language), duration, mustJSON(audio), mustJSON(chapters), mustJSON(metadata.Tags), mustJSON(metadata.Tags), mustJSON(metadata.Genres), mustJSON(metadata.Genres), mustJSON(metadata.Narrators), mustJSON(metadata.Narrators), now, mediaID); err != nil {
				return imported{}, err
			}
		}
		// BookScanner update reconciliation only adds scanned associations; it
		// does not delete joins absent from metadata (BookScanner.js:85-147).
		for _, authorName := range authors {
			authorName = strings.TrimSpace(authorName)
			if authorName == "" {
				continue
			}
			var aid string
			if err = tx.QueryRowContext(ctx, `SELECT id FROM authors WHERE name=? AND libraryId=? LIMIT 1`, authorName, l.id).Scan(&aid); err == sql.ErrNoRows {
				aid = uuid.NewString()
				authorCreated := time.Now().UTC().UnixMilli() + int64(len(affectedAuthors))
				authorTime := dbtime.Format(time.UnixMilli(authorCreated))
				if _, err = tx.ExecContext(ctx, `INSERT INTO authors(id,name,lastFirst,libraryId,createdAt,updatedAt) VALUES(?,?,?,?,?,?)`, aid, authorName, lastFirst(authorName), l.id, authorTime, authorTime); err != nil {
					return imported{}, err
				}
				addedAuthors = append(addedAuthors, map[string]any{"id": aid, "asin": nil, "name": authorName, "description": nil, "imagePath": nil, "libraryId": l.id, "addedAt": time.Now().UnixMilli(), "updatedAt": time.Now().UnixMilli()})
			} else if err != nil {
				return imported{}, err
			}
			affectedAuthors = append(affectedAuthors, aid)
			linkResult, linkErr := tx.ExecContext(ctx, `INSERT INTO bookAuthors(id,bookId,authorId,createdAt) SELECT ?,?,?,? WHERE NOT EXISTS(SELECT 1 FROM bookAuthors WHERE bookId=? AND authorId=?)`, uuid.NewString(), mediaID, aid, now, mediaID, aid)
			if linkErr != nil {
				return imported{}, linkErr
			}
			if rows, rowsErr := linkResult.RowsAffected(); rowsErr == nil && rows > 0 {
				// Only an association change on an existing book updates an
				// existing author's count; new authors use author_added instead.
				var wasCreated bool
				for _, payload := range addedAuthors {
					if payload["id"] == aid {
						wasCreated = true
						break
					}
				}
				if !wasCreated {
					existingAffectedAuthors = append(existingAffectedAuthors, aid)
				}
			}
		}
		if series != "" {
			var sid string
			if err = tx.QueryRowContext(ctx, `SELECT id FROM series WHERE name=? AND libraryId=? LIMIT 1`, series, l.id).Scan(&sid); err == sql.ErrNoRows {
				sid = uuid.NewString()
				if _, err = tx.ExecContext(ctx, `INSERT INTO series(id,name,nameIgnorePrefix,libraryId,createdAt,updatedAt) VALUES(?,?,?,?,?,?)`, sid, series, scannerTitleSortKey(series, l.sortingPrefixes), l.id, now, now); err != nil {
					return imported{}, err
				}
				addedSeries = append(addedSeries, map[string]any{"id": sid, "name": series, "nameIgnorePrefix": series, "description": nil, "addedAt": time.Now().UnixMilli(), "updatedAt": time.Now().UnixMilli(), "libraryId": l.id})
			} else if err != nil {
				return imported{}, err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO bookSeries(id,bookId,seriesId,sequence,createdAt) SELECT ?,?,?,?,? WHERE NOT EXISTS(SELECT 1 FROM bookSeries WHERE bookId=? AND seriesId=?)`, uuid.NewString(), mediaID, sid, seq, now, mediaID, sid); err != nil {
				return imported{}, err
			}
		}
	}
	extra := `{}`
	if added {
		_, err = tx.ExecContext(ctx, `INSERT INTO libraryItems(id,ino,libraryId,libraryFolderId,path,relPath,isFile,mtime,ctime,birthtime,createdAt,updatedAt,lastScan,lastScanVersion,isMissing,isInvalid,mediaType,size,extraData,libraryFiles,mediaId,title,titleIgnorePrefix,authorNamesFirstLast,authorNamesLastFirst) VALUES(?,?,?,?,?,?,?, ?,?,?,?,?,?,?,0,0,?,?,?,?,?,?,?, ?,?)`, itemID, itemIno, l.id, g.folder.id, g.path, rel, boolInt(g.isFile), mtime, ctime, birthtime, now, now, now, "2.37.2", strings.ToLower(l.mediaType), size, extra, string(fj), mediaID, title, scannerTitleSortKey(title, l.sortingPrefixes), author, lastFirst(author))
		if err != nil {
			return imported{}, err
		}
	} else if force || oldPath != g.path || oldRel != rel || oldIno != itemIno || oldMediaType != l.mediaType || !sameBytes(oldFiles, fj) {
		_, err = tx.ExecContext(ctx, `UPDATE libraryItems SET path=?,ino=?,libraryFolderId=?,relPath=?,isFile=?,mtime=?,ctime=?,birthtime=?,updatedAt=?,lastScan=?,lastScanVersion=?,isMissing=0,size=?,libraryFiles=?,title=?,titleIgnorePrefix=?,authorNamesFirstLast=?,authorNamesLastFirst=? WHERE id=?`, g.path, itemIno, g.folder.id, rel, boolInt(g.isFile), mtime, ctime, birthtime, now, now, "2.37.2", size, string(fj), title, scannerTitleSortKey(title, l.sortingPrefixes), author, lastFirst(author), itemID)
		if err != nil {
			return imported{}, err
		}
	} else {
		// A no-op scan must not rewrite item bookkeeping.
	}
	if err = tx.Commit(); err != nil {
		return imported{}, err
	}
	if l.mediaType == "book" && (added || force || !sameBytes(oldFiles, fj)) {
		var settings map[string]any
		settings = map[string]any{"storeMetadataWithItem": false, "metadataFileFormat": "json"}
		var settingsRaw []byte
		if e := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='server-settings'`).Scan(&settingsRaw); e == nil {
			_ = json.Unmarshal(settingsRaw, &settings)
		}
		if item, e := s.items.ItemJSON(ctx, itemID, true); e == nil {
			_ = WriteItemMetadata(ctx, s.db, s.cfg, itemID, item, settings)
		}
	}
	if s.emit != nil {
		for _, payload := range addedAuthors {
			s.emit("*", "author_added", payload)
		}
		for _, payload := range addedSeries {
			s.emit("*", "series_added", payload)
		}
		if len(existingAffectedAuthors) > 0 {
			counts := make([]map[string]any, 0, len(existingAffectedAuthors))
			for _, aid := range existingAffectedAuthors {
				var n int
				if s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bookAuthors WHERE authorId=?`, aid).Scan(&n) == nil && n > 0 {
					counts = append(counts, map[string]any{"id": aid, "numBooks": n})
				}
			}
			if len(counts) > 0 {
				s.emit("*", "authors_num_books_updated", map[string]any{"libraryId": l.id, "authors": counts})
			}
		}
	}
	_ = s.cleanupAssociations(ctx, l.id)
	return imported{id: itemID, added: added, updated: !added && (!sameBytes(oldFiles, fj) || oldPath != g.path || oldRel != rel || oldIno != itemIno || oldMediaType != l.mediaType)}, nil
}

func (s *Scanner) priorAudioEntry(mediaID, currentPath, currentIno string, mtime int64) (map[string]any, bool) {
	var raw []byte
	if s.db.QueryRow(`SELECT audioFiles FROM books WHERE id=?`, mediaID).Scan(&raw) != nil {
		return nil, false
	}
	var entries []map[string]any
	if json.Unmarshal(raw, &entries) != nil {
		return nil, false
	}
	// BookScanner.js:97-101 matches a changed file by path, then inode.
	for _, entry := range entries {
		if meta, ok := entry["metadata"].(map[string]any); ok && meta["path"] == currentPath {
			return entry, audioEntryMtime(entry) == mtime
		}
	}
	for _, entry := range entries {
		if entry["ino"] == currentIno {
			return entry, audioEntryMtime(entry) == mtime
		}
	}
	return nil, false
}

func audioEntryMtime(entry map[string]any) int64 {
	meta, _ := entry["metadata"].(map[string]any)
	m, _ := meta["metadata"].(map[string]any)
	switch value := m["mtimeMs"].(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case int:
		return int64(value)
	}
	return -1
}

// titleIgnorePrefix mirrors server/utils/index.js:182-202; prefixes are
// configured, case-insensitive at the start, and require a following space.
func scannerTitleSortKey(title string, prefixes []string) string {
	for _, prefix := range prefixes {
		if len(title) > len(prefix) && strings.EqualFold(title[:len(prefix)], prefix) && title[len(prefix)] == ' ' {
			return title[len(prefix)+1:]
		}
	}
	return title
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// Group files are captured during discovery; directory reads here would lose
// upstream traversal order and attach files outside the claimed item path.

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func scannableCompanion(ext string) bool {
	switch strings.ToLower(ext) {
	case ".txt", ".nfo", ".opf", ".abs", ".xml", ".json", ".png", ".jpg", ".jpeg", ".webp":
		return true
	}
	return false
}

func nullableString(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

// readABSMetadata prefers a readable in-item metadata.json and falls back to
// an existing item's external sidecar only when that file is unavailable.
func readABSMetadata(g *group, itemID, metadataPath string) (bookMetadata, bool) {
	var result bookMetadata
	for _, f := range g.companions {
		if strings.EqualFold(filepath.Base(f.path), "metadata.json") {
			if raw, err := os.ReadFile(f.path); err == nil {
				parseAbsMetadata(raw, &result)
				return result, true
			}
		}
	}
	if itemID == "" || metadataPath == "" {
		return result, false
	}
	path := filepath.Join(metadataPath, "items", itemID, "metadata.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return result, false
	}
	parseAbsMetadata(raw, &result)
	return result, true
}

var defaultMetadataSourceOrder = []string{"folderStructure", "audioMetatags", "nfoFile", "txtFiles", "opfFile", "absMetadata"}

func defaultMetadataPrecedence() []string {
	return append([]string(nil), defaultMetadataSourceOrder...)
}

func mergeBookMetadata(title, author, series, sequence string, g *group, order []string, audioTags map[string]any) bookMetadata {
	m := bookMetadata{Title: title, Authors: parseNameList(author)}
	if series != "" {
		m.Series = []seriesMetadata{{Name: series, Sequence: sequence}}
	}
	for _, source := range order {
		switch source {
		case "folderStructure":
		case "audioMetatags":
			if len(g.files) > 0 {
				applyAudioMetadata(audioTags, &m)
			}
		case "nfoFile":
			applyNFO(g, &m)
		case "txtFiles":
			applyTextFiles(g, &m)
		case "opfFile":
			applyOPFFiles(g, &m)
		case "absMetadata":
			// This compatibility helper has no external item ID; the integrated
			// path uses mergeBookMetadataWithABS for the external fallback.
		default:
			// Unknown source names are intentionally ignored, like LibraryScanner's source dispatcher.
		}
	}
	return m
}

func mergeBookMetadataWithABS(title, author, series, sequence string, g *group, order []string, audioTags map[string]any, abs bookMetadata, hasABS bool) bookMetadata {
	m := bookMetadata{Title: title, Authors: parseNameList(author)}
	if series != "" {
		m.Series = []seriesMetadata{{Name: series, Sequence: sequence}}
	}
	for _, source := range order {
		switch source {
		case "folderStructure":
		case "audioMetatags":
			if len(g.files) > 0 {
				applyAudioMetadata(audioTags, &m)
			}
		case "nfoFile":
			applyNFO(g, &m)
		case "txtFiles":
			applyTextFiles(g, &m)
		case "opfFile":
			applyOPFFiles(g, &m)
		case "absMetadata":
			if hasABS {
				applyABSMetadata(abs, &m)
			}
		default:
		}
	}
	return m
}
func applyTextFiles(g *group, dst *bookMetadata) {
	for _, f := range g.companions {
		switch strings.ToLower(filepath.Base(f.path)) {
		case "desc.txt":
			if raw, e := os.ReadFile(f.path); e == nil && strings.TrimSpace(string(raw)) != "" {
				dst.Description = strings.TrimSpace(string(raw))
			}
		case "reader.txt":
			if raw, e := os.ReadFile(f.path); e == nil {
				lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
				if len(lines) > 0 && strings.TrimSpace(lines[0]) != "" {
					dst.Narrators = parseNameList(strings.TrimSpace(lines[0]))
				}
			}
		}
	}
}
func applyNFO(g *group, dst *bookMetadata) {
	for _, f := range g.companions {
		if strings.EqualFold(filepath.Ext(f.path), ".nfo") {
			raw, e := os.ReadFile(f.path)
			if e != nil {
				continue
			}
			values := map[string]string{}
			for _, line := range strings.Split(string(raw), "\n") {
				if i := strings.Index(line, ":"); i >= 0 {
					values[strings.ToLower(strings.TrimSpace(line[:i]))] = strings.TrimSpace(line[i+1:])
				}
			}
			if v := values["title"]; v != "" {
				dst.Title = v
			}
			if v := values["subtitle"]; v != "" {
				dst.Subtitle = v
			}
			if v := values["publishedyear"]; v != "" {
				dst.PublishedYear = v
			}
			if v := values["description"]; v != "" {
				dst.Description = v
			}
			if v := values["tag"]; v != "" {
				dst.Tags = parseNameList(v)
			}
			if v := values["author"]; v != "" {
				dst.Authors = parseNameList(v)
			}
			if v := values["narrator"]; v != "" {
				dst.Narrators = parseNameList(v)
			}
			if v := values["series name"]; v != "" {
				dst.Series = []seriesMetadata{{Name: v, Sequence: values["position in series"]}}
			}
			if v := values["genre"]; v != "" {
				dst.Genres = parseNameList(v)
				dst.Tags = append([]string(nil), dst.Genres...)
			}
			if v := values["publisher"]; v != "" {
				dst.Publisher = v
			}
			if v := values["asin"]; v != "" {
				dst.ASIN = v
			}
			if v := values["isbn"]; v != "" {
				dst.ISBN = v
			}
			if v := values["language"]; v != "" {
				dst.Language = v
			}
			break
		}
	}
}
func applyOPFFiles(g *group, dst *bookMetadata) {
	for _, f := range g.companions {
		if strings.EqualFold(filepath.Ext(f.path), ".opf") {
			if raw, e := os.ReadFile(f.path); e == nil {
				var parsed bookMetadata
				parseOPF(raw, &parsed)
				applyNonemptyMetadata(parsed, dst)
			}
			break
		}
	}
}
func applyNonemptyMetadata(src bookMetadata, dst *bookMetadata) {
	if src.Title != "" {
		dst.Title = src.Title
	}
	if src.Subtitle != "" {
		dst.Subtitle = src.Subtitle
	}
	if src.PublishedYear != "" {
		dst.PublishedYear = src.PublishedYear
	}
	if src.PublishedDate != "" {
		dst.PublishedDate = src.PublishedDate
	}
	if src.Publisher != "" {
		dst.Publisher = src.Publisher
	}
	if src.Description != "" {
		dst.Description = src.Description
	}
	if src.ISBN != "" {
		dst.ISBN = src.ISBN
	}
	if src.ASIN != "" {
		dst.ASIN = src.ASIN
	}
	if src.Language != "" {
		dst.Language = src.Language
	}
	if len(src.Authors) > 0 {
		dst.Authors = src.Authors
	}
	if len(src.Narrators) > 0 {
		dst.Narrators = src.Narrators
	}
	if len(src.Tags) > 0 {
		dst.Tags = src.Tags
	}
	if len(src.Genres) > 0 {
		dst.Genres = src.Genres
	}
	if len(src.Series) > 0 {
		dst.Series = src.Series
	}
	if len(src.Chapters) > 0 {
		dst.Chapters = src.Chapters
	}
}

func applyABSMetadata(src bookMetadata, dst *bookMetadata) {
	for key, value := range map[string]string{"title": src.Title, "subtitle": src.Subtitle, "publishedYear": src.PublishedYear, "publishedDate": src.PublishedDate, "publisher": src.Publisher, "description": src.Description, "isbn": src.ISBN, "asin": src.ASIN, "language": src.Language} {
		if src.absPresent[key] {
			switch key {
			case "title":
				dst.Title = value
			case "subtitle":
				dst.Subtitle = value
			case "publishedYear":
				dst.PublishedYear = value
			case "publishedDate":
				dst.PublishedDate = value
			case "publisher":
				dst.Publisher = value
			case "description":
				dst.Description = value
			case "isbn":
				dst.ISBN = value
			case "asin":
				dst.ASIN = value
			case "language":
				dst.Language = value
			}
		}
	}
	if len(src.Authors) > 0 {
		dst.Authors = src.Authors
	}
	if len(src.Narrators) > 0 {
		dst.Narrators = src.Narrators
	}
	if len(src.Tags) > 0 {
		dst.Tags = src.Tags
	}
	if len(src.Genres) > 0 {
		dst.Genres = src.Genres
	}
	if len(src.Series) > 0 {
		dst.Series = src.Series
	}
	if len(src.Chapters) > 0 {
		dst.Chapters = src.Chapters
	}
}

func parseAbsMetadata(raw []byte, dst *bookMetadata) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return
	}
	dst.absPresent = map[string]bool{}
	if nested := obj["metadata"]; len(nested) > 0 {
		var m map[string]json.RawMessage
		if json.Unmarshal(nested, &m) == nil {
			for k, v := range m {
				if _, ok := obj[k]; !ok {
					obj[k] = v
				}
			}
		}
	}
	decode := func(key string, out any) {
		if v := obj[key]; len(v) > 0 {
			_ = json.Unmarshal(v, out)
		}
	}
	decodeString := func(key string, out *string) {
		if v := obj[key]; len(v) > 0 && string(v) != "null" {
			dst.absPresent[key] = true
			_ = json.Unmarshal(v, out)
		}
	}
	decodeArray := func(key string, out *[]string) {
		var values []string
		if v := obj[key]; len(v) > 0 && json.Unmarshal(v, &values) == nil && len(values) > 0 {
			*out = values
		}
	}
	decodeString("title", &dst.Title)
	decodeString("subtitle", &dst.Subtitle)
	decodeString("publishedYear", &dst.PublishedYear)
	decodeString("publishedDate", &dst.PublishedDate)
	decodeString("publisher", &dst.Publisher)
	decodeString("description", &dst.Description)
	decodeString("isbn", &dst.ISBN)
	decodeString("asin", &dst.ASIN)
	decodeString("language", &dst.Language)
	decodeArray("authors", &dst.Authors)
	decodeArray("narrators", &dst.Narrators)
	decodeArray("tags", &dst.Tags)
	decodeArray("genres", &dst.Genres)
	var chapters []map[string]any
	if v := obj["chapters"]; len(v) > 0 && json.Unmarshal(v, &chapters) == nil && len(chapters) > 0 {
		dst.Chapters = chapters
	}
	var names []string
	decode("series", &names)
	var objects []seriesMetadata
	decode("series", &objects)
	if len(objects) > 0 {
		dst.Series = objects
	} else if len(names) > 0 {
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name != "" {
				parsed := name
				if i := strings.LastIndex(parsed, " #"); i > 0 && !strings.ContainsAny(parsed[i+2:], "# \t\r\n") {
					dst.Series = append(dst.Series, seriesMetadata{Name: strings.TrimSpace(parsed[:i]), Sequence: parsed[i+2:]})
				} else {
					dst.Series = append(dst.Series, seriesMetadata{Name: name})
				}
			}
		}
	}
}

func parseOPF(raw []byte, dst *bookMetadata) {
	type value struct {
		Text    string `xml:",chardata"`
		ID      string `xml:"id,attr"`
		Scheme  string `xml:"scheme,attr"`
		Name    string `xml:"name,attr"`
		Content string `xml:"content,attr"`
		Role    string `xml:"role,attr"`
	}
	var doc struct {
		Metadata struct {
			Titles       []value `xml:"title"`
			Subtitles    []value `xml:"subtitle"`
			Creators     []value `xml:"creator"`
			Dates        []value `xml:"date"`
			Publishers   []value `xml:"publisher"`
			Descriptions []value `xml:"description"`
			Identifiers  []value `xml:"identifier"`
			Languages    []value `xml:"language"`
			Subjects     []value `xml:"subject"`
			Metas        []value `xml:"meta"`
		} `xml:"metadata"`
	}
	if xml.Unmarshal(raw, &doc) != nil {
		return
	}
	m := doc.Metadata
	first := func(a []value) string {
		if len(a) > 0 {
			return strings.TrimSpace(a[0].Text)
		}
		return ""
	}
	if dst.Title == "" {
		dst.Title = first(m.Titles)
	}
	if dst.Subtitle == "" {
		dst.Subtitle = first(m.Subtitles)
	}
	if dst.Publisher == "" {
		dst.Publisher = first(m.Publishers)
	}
	if dst.Description == "" {
		dst.Description = first(m.Descriptions)
	}
	if dst.Language == "" {
		dst.Language = first(m.Languages)
	}
	if y := first(m.Dates); len(y) >= 4 && dst.PublishedYear == "" {
		dst.PublishedYear = y[:4]
	}
	for _, v := range m.Identifiers {
		if strings.EqualFold(v.Scheme, "ISBN") && dst.ISBN == "" {
			dst.ISBN = strings.TrimSpace(v.Text)
		}
		if strings.EqualFold(v.Scheme, "ASIN") && dst.ASIN == "" {
			dst.ASIN = strings.TrimSpace(v.Text)
		}
	}
	if len(dst.Authors) == 0 {
		for _, v := range m.Creators {
			role := strings.ToLower(v.Role)
			if role == "aut" {
				if strings.TrimSpace(v.Text) != "" {
					dst.Authors = append(dst.Authors, strings.TrimSpace(v.Text))
				}
			} else if role == "nrt" {
				dst.Narrators = append(dst.Narrators, strings.TrimSpace(v.Text))
			}
		}
	}
	if len(dst.Narrators) == 0 {
		for _, v := range m.Creators {
			if strings.EqualFold(v.Role, "nrt") && strings.TrimSpace(v.Text) != "" {
				dst.Narrators = append(dst.Narrators, strings.TrimSpace(v.Text))
			}
		}
	}
	if len(dst.Genres) == 0 {
		for _, v := range m.Subjects {
			if strings.TrimSpace(v.Text) != "" {
				dst.Genres = append(dst.Genres, strings.TrimSpace(v.Text))
			}
		}
	}
	if len(dst.Tags) == 0 {
		for _, v := range m.Metas {
			if strings.EqualFold(v.Name, "tags") && strings.TrimSpace(v.Content) != "" {
				dst.Tags = parseNameList(v.Content)
			}
		}
	}
	if len(dst.Series) == 0 {
		seriesIndex := ""
		for _, v := range m.Metas {
			if strings.EqualFold(v.Name, "calibre:series") && strings.TrimSpace(v.Content) != "" {
				seriesIndex = ""
				dst.Series = append(dst.Series, seriesMetadata{Name: strings.TrimSpace(v.Content)})
			} else if strings.EqualFold(v.Name, "calibre:series_index") && strings.TrimSpace(v.Content) != "" {
				seriesIndex = strings.TrimSpace(v.Content)
			}
		}
		if len(dst.Series) == 1 {
			dst.Series[0].Sequence = seriesIndex
		}
	}
}

func timeString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return dbtime.Format(t)
}
func makeFileMeta(root string, f file) map[string]any {
	rel := f.relPath
	if rel == "" {
		rel, _ = filepath.Rel(root, f.path)
	}
	ext := strings.ToLower(filepath.Ext(f.path))
	mtime, ctime, birthtime := statTimes(f.info)
	return map[string]any{"ino": inode(f.info), "metadata": map[string]any{"filename": filepath.Base(f.path), "ext": ext, "path": f.path, "relPath": filepath.ToSlash(rel), "size": f.info.Size(), "mtimeMs": ms(mtime), "ctimeMs": ms(ctime), "birthtimeMs": ms(birthtime)}, "isSupplementary": nil, "addedAt": ms(mtime), "updatedAt": ms(mtime), "fileType": fileTypeForExtension(ext)}
}

func statTimes(info os.FileInfo) (mtime, ctime, birthtime time.Time) {
	mtime = info.ModTime()
	ctime = statChangeTime(info)
	birthtime = ctime
	return
}
func fileTypeForExtension(ext string) string {
	switch strings.ToLower(ext) {
	case ".mp3", ".m4b", ".m4a", ".flac", ".opus", ".ogg", ".oga", ".mp4", ".aac", ".wma", ".aiff", ".aif", ".wav", ".webm", ".webma", ".mka", ".awb", ".caf", ".mpg", ".mpeg":
		return "audio"
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".tif", ".tiff":
		return "image"
	case ".epub", ".pdf", ".mobi", ".azw", ".azw3", ".cbz", ".cbr":
		return "ebook"
	case ".json", ".opf", ".nfo", ".abs":
		return "metadata"
	case ".txt":
		return "text"
	default:
		return "unknown"
	}
}
func inode(info os.FileInfo) string {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return strconv.FormatUint(st.Ino, 10)
	}
	return ""
}

type probeStream struct {
	Codec         string            `json:"codec_name"`
	CodecLong     string            `json:"codec_long_name"`
	Type          string            `json:"codec_type"`
	Index         int               `json:"index"`
	Channels      int               `json:"channels"`
	ChannelLayout string            `json:"channel_layout"`
	TimeBase      string            `json:"time_base"`
	BitRate       string            `json:"bit_rate"`
	Language      string            `json:"language"`
	Tags          map[string]string `json:"tags"`
	Disposition   struct {
		AttachedPic int `json:"attached_pic"`
		Default     int `json:"default"`
	} `json:"disposition"`
}
type probeChapter struct {
	StartTime string            `json:"start_time"`
	EndTime   string            `json:"end_time"`
	Start     json.Number       `json:"start"`
	End       json.Number       `json:"end"`
	TimeBase  string            `json:"time_base"`
	Tags      map[string]string `json:"tags"`
}

// probeAudio shapes the imported audio file per the upstream AudioFile object
// (server/objects/files/AudioFile.js:113-136): metadata is the flat
// FileMetadata object; the inode belongs top-level only. meta carries the flat
// file metadata plus an optional transient "scanMetadata" map used for
// filename track/disc parsing and removed from the persisted entry.
func probeAudio(ctx context.Context, p string, meta map[string]any, fileIno string) (map[string]any, error) {
	ffprobe := strings.TrimSpace(os.Getenv("FFPROBE_PATH"))
	if ffprobe == "" {
		ffprobe = "ffprobe"
	}
	cmd := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_format", "-show_streams", "-show_chapters", "-of", "json", p)
	raw, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var res struct {
		Format struct {
			Name     string            `json:"format_name"`
			LongName string            `json:"format_long_name"`
			Duration string            `json:"duration"`
			BitRate  string            `json:"bit_rate"`
			Tags     map[string]string `json:"tags"`
		} `json:"format"`
		Streams  []probeStream  `json:"streams"`
		Chapters []probeChapter `json:"chapters"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	duration, _ := strconv.ParseFloat(res.Format.Duration, 64)
	format := res.Format.LongName
	if format == "" {
		format = res.Format.Name
	}
	var ext, filename string
	if value, ok := meta["ext"].(string); ok {
		ext = value
	}
	if value, ok := meta["filename"].(string); ok {
		filename = value
	}
	persistedMeta := make(map[string]any, len(meta))
	for key, value := range meta {
		if key == "scanMetadata" {
			continue
		}
		persistedMeta[key] = value
	}
	var audio *probeStream
	var embedded any
	for i := range res.Streams {
		s := &res.Streams[i]
		if s.Type == "video" && s.Disposition.AttachedPic == 1 {
			embedded = s.Codec
		}
		if s.Type == "audio" && (audio == nil || s.Disposition.Default == 1) {
			audio = s
			if s.Disposition.Default == 1 {
				break
			}
		}
	}
	if audio == nil {
		// Upstream rejects files without an audio stream outright
		// (AudioFileScanner.js:156-179).
		return nil, nil
	}
	bitRate, _ := strconv.ParseInt(res.Format.BitRate, 10, 64)
	var normalizedBitRate any
	if bitRate > 0 {
		normalizedBitRate = bitRate
	}
	tags := res.Format.Tags
	out := map[string]any{"index": 1, "ino": fileIno, "metadata": persistedMeta, "addedAt": time.Now().UnixMilli(), "updatedAt": time.Now().UnixMilli(), "trackNumFromMeta": nil, "discNumFromMeta": nil, "trackNumFromFilename": nil, "discNumFromFilename": nil, "manuallyVerified": false, "exclude": false, "error": nil, "format": format, "duration": duration, "bitRate": normalizedBitRate, "language": nil, "codec": nil, "timeBase": nil, "channels": nil, "channelLayout": nil, "chapters": []map[string]any{}, "embeddedCoverArt": embedded, "metaTags": map[string]any{}, "mimeType": audioMimeType(ext)}
	out["codec"] = audio.Codec
	out["timeBase"] = audio.TimeBase
	if audio.Channels > 0 {
		out["channels"] = audio.Channels
	}
	if audio.ChannelLayout != "" {
		out["channelLayout"] = strings.Split(audio.ChannelLayout, "(")[0]
	}
	// Upstream language comes from the stream tags (prober.js:92).
	if language, ok := audio.Tags["language"]; ok && strings.TrimSpace(language) != "" {
		out["language"] = strings.TrimSpace(language)
	}
	if streamRate, e := strconv.ParseInt(audio.BitRate, 10, 64); e == nil && streamRate > 0 {
		out["bitRate"] = streamRate
	}
	if tags == nil {
		tags = audio.Tags
	}
	metaTags := parseAudioTags(tags)
	out["metaTags"] = metaTags
	if value, ok := metaTags["tagTrack"].(string); ok {
		out["trackNumFromMeta"] = firstTrackNumber(value)
	}
	if value, ok := metaTags["tagDisc"].(string); ok {
		out["discNumFromMeta"] = firstTrackNumber(value)
	}
	var scanTitle, scanAuthor, scanSeries, scanYear string
	if value, ok := meta["scanMetadata"].(map[string]any); ok {
		scanTitle, _ = value["title"].(string)
		scanAuthor, _ = value["author"].(string)
		scanSeries, _ = value["series"].(string)
		scanYear, _ = value["publishedYear"].(string)
	}
	out["trackNumFromFilename"], out["discNumFromFilename"] = filenameNumbers(filename, filepath.Dir(p), scanTitle, scanAuthor, scanSeries, scanYear)
	chapters := make([]map[string]any, 0, len(res.Chapters))
	for i, ch := range res.Chapters {
		start, _ := strconv.ParseFloat(ch.StartTime, 64)
		end, _ := strconv.ParseFloat(ch.EndTime, 64)
		if ch.StartTime == "" {
			start = chapterSeconds(ch.Start, ch.TimeBase)
		}
		if ch.EndTime == "" {
			end = chapterSeconds(ch.End, ch.TimeBase)
		}
		chapters = append(chapters, map[string]any{"start": start, "end": end, "title": strings.TrimSpace(ch.Tags["title"]), "id": i})
	}
	sort.Slice(chapters, func(i, j int) bool { return chapters[i]["start"].(float64) < chapters[j]["start"].(float64) })
	for i := range chapters {
		chapters[i]["id"] = i
	}
	out["chapters"] = chapters
	if filename != "" {
		out["trackNumFromFilename"], out["discNumFromFilename"] = filenameNumbers(filename, filepath.Dir(p), "", "", "", "")
	}
	return out, nil
}

func chapterSeconds(value json.Number, timeBase string) float64 {
	n, _ := value.Int64()
	den := 1.0
	if parts := strings.Split(timeBase, "/"); len(parts) == 2 {
		if parsed, err := strconv.ParseFloat(parts[1], 64); err == nil && parsed != 0 {
			den = parsed
		}
	}
	return float64(n) / den
}
func audioMimeType(ext string) string {
	switch strings.TrimPrefix(strings.ToLower(ext), ".") {
	case "m4b", "m4a", "mp4":
		return "audio/mp4"
	case "ogg", "oga", "opus":
		return "audio/ogg"
	case "aac":
		return "audio/aac"
	case "flac":
		return "audio/flac"
	case "wma":
		return "audio/x-ms-wma"
	case "aiff", "aif":
		return "audio/x-aiff"
	case "webm", "webma":
		return "audio/webm"
	case "mka":
		return "audio/x-matroska"
	case "awb":
		return "audio/amr-wb"
	case "caf":
		return "audio/x-caf"
	case "mpg", "mpeg":
		return "audio/mpeg"
	default:
		return "audio/mpeg"
	}
}
func firstTrackNumber(value string) any {
	part := strings.Split(value, "/")[0]
	number, err := strconv.Atoi(strings.TrimSpace(part))
	if err != nil {
		return nil
	}
	return number
}
func trackNumbersFromFilename(filename string) (any, any) {
	return filenameNumbers(filename, "", "", "", "", "")
}

func audioFilename(af map[string]any) string {
	if meta, ok := af["metadata"].(map[string]any); ok {
		if filename, ok := meta["filename"].(string); ok {
			return strings.TrimSuffix(filename, filepath.Ext(filename))
		}
	}
	return ""
}

func filenameNumbers(filename, dir, title, author, series, year string) (any, any) {
	base := strings.TrimSuffix(filepath.Base(filename), filepath.Ext(filename))
	for _, remove := range []string{title, author, series, year} {
		if remove != "" {
			base = strings.Replace(base, remove, "", 1)
		}
	}
	disc := any(nil)
	discRE := regexp.MustCompile(`(?i)\b(disc|cd) ?(\d\d?)\b`)
	if m := discRE.FindStringSubmatch(base); len(m) > 2 {
		if n, e := strconv.Atoi(m[2]); e == nil {
			disc = n
		}
		base = strings.Replace(base, m[0], "", 1)
	}
	if dirRE := regexp.MustCompile(`(?i)^(cd|dis[ck])\s*\d{1,3}$`); dirRE.MatchString(filepath.Base(dir)) {
		n, _ := strconv.Atoi(regexp.MustCompile(`(?i)^(cd|dis[ck])\s*`).ReplaceAllString(filepath.Base(dir), ""))
		disc = n
	}
	numRE := regexp.MustCompile(`\d{1,4}`)
	m := numRE.FindString(base)
	if m == "" {
		return nil, disc
	}
	n, _ := strconv.Atoi(m)
	return n, disc
}

func smartAudioOrder(audio []map[string]any) []map[string]any {
	selectKey := func(meta, filename string) string {
		all := func(key string) bool {
			for _, af := range audio {
				if af[key] == nil {
					return false
				}
			}
			return true
		}
		sequential := func(key string) bool {
			nums := []int{}
			for _, af := range audio {
				if n, ok := af[key].(int); ok {
					nums = append(nums, n)
				} else {
					return false
				}
			}
			sort.Ints(nums)
			for i := 1; i < len(nums); i++ {
				if nums[i]-nums[i-1] > 1 {
					return false
				}
			}
			return len(nums) > 0
		}
		if all(meta) && sequential(meta) {
			return meta
		}
		if all(filename) && sequential(filename) {
			return filename
		}
		return ""
	}
	disc := selectKey("discNumFromMeta", "discNumFromFilename")
	track := "trackNumFromMeta"
	fm, mm := map[int]bool{}, map[int]bool{}
	for _, a := range audio {
		if n, ok := a["trackNumFromFilename"].(int); ok {
			fm[n] = true
		}
		if n, ok := a["trackNumFromMeta"].(int); ok {
			mm[n] = true
		}
	}
	if len(fm) > len(mm) {
		track = "trackNumFromFilename"
	}
	less := func(a, b map[string]any) bool {
		if disc != "" {
			x, _ := a[disc].(int)
			y, _ := b[disc].(int)
			if x != y {
				return x < y
			}
		}
		x, xok := a[track].(int)
		y, yok := b[track].(int)
		if !xok || !yok {
			return false
		}
		if x != y {
			return x < y
		}
		am := a["metadata"].(map[string]any)
		bm := b["metadata"].(map[string]any)
		return am["filename"].(string) < bm["filename"].(string)
	}
	sort.SliceStable(audio, func(i, j int) bool { return less(audio[i], audio[j]) })
	for i, a := range audio {
		a["index"] = i + 1
	}
	return audio
}

func buildChapters(audio []map[string]any, bookTitle string) []map[string]any {
	out := []map[string]any{}
	if len(audio) == 0 {
		return out
	}
	chapters := func(a map[string]any) []map[string]any { c, _ := a["chapters"].([]map[string]any); return c }
	first := chapters(audio[0])
	same := len(audio) > 1 && len(first) > 0 && len(first) == len(chapters(audio[1]))
	if same {
		for i, c := range first {
			d := chapters(audio[1])[i]
			if c["title"] != d["title"] || c["start"] != d["start"] {
				same = false
				break
			}
		}
	}
	if len(first) > 0 {
		if len(audio) == 1 || same {
			for _, c := range first {
				out = append(out, c)
			}
			return out
		}
		offset := 0.0
		for _, a := range audio {
			d, _ := a["duration"].(float64)
			if d == 0 {
				continue
			}
			for _, c := range chapters(a) {
				st, _ := c["start"].(float64)
				en, _ := c["end"].(float64)
				if en-st < .1 {
					continue
				}
				cp := map[string]any{}
				for k, v := range c {
					cp[k] = v
				}
				cp["start"] = st + offset
				cp["end"] = en + offset
				cp["id"] = len(out)
				out = append(out, cp)
			}
			offset += d
		}
		return out
	}
	if len(audio) <= 1 {
		return out
	}
	uniqueTitles := map[string]bool{}
	useMetaTitles := len(audio) > 1
	for _, a := range audio {
		m, _ := a["metaTags"].(map[string]any)
		tag, _ := m["tagTitle"].(string)
		if tag == "" || tag == bookTitle {
			useMetaTitles = false
		}
		if tag != "" && tag != bookTitle {
			uniqueTitles[tag] = true
		}
	}
	if len(uniqueTitles) != len(audio) {
		useMetaTitles = false
	}
	offset := 0.0
	for _, a := range audio {
		d, _ := a["duration"].(float64)
		if d <= 0 {
			continue
		}
		m, _ := a["metadata"].(map[string]any)
		filename, _ := m["filename"].(string)
		title := strings.TrimSuffix(filename, filepath.Ext(filename))
		if useMetaTitles {
			m, _ := a["metaTags"].(map[string]any)
			title, _ = m["tagTitle"].(string)
		}
		out = append(out, map[string]any{"id": len(out), "start": offset, "end": offset + d, "title": title})
		offset += d
	}
	return out
}

var audioTagAliases = map[string][]string{"tagEncoder": {"encoder", "tsse", "tss"}, "tagEncodedBy": {"encoded_by", "tenc", "ten"}, "tagTitle": {"title", "tit2", "tt2"}, "tagTitleSort": {"title-sort", "tsot"}, "tagSubtitle": {"subtitle", "tit3", "tt3"}, "tagTrack": {"track", "trck", "trk"}, "tagDisc": {"discnumber", "disc", "disk", "tpos", "tpa"}, "tagAlbum": {"album", "talb", "tal"}, "tagAlbumSort": {"album-sort", "tsoa"}, "tagArtist": {"artist", "tpe1", "tp1"}, "tagArtistSort": {"artist-sort", "tsop"}, "tagAlbumArtist": {"albumartist", "album_artist", "tpe2"}, "tagDate": {"date", "tyer", "tye"}, "tagComposer": {"composer", "tcom", "tcm"}, "tagPublisher": {"publisher", "tpub", "tpb"}, "tagComment": {"comment", "comm", "com"}, "tagDescription": {"description", "desc"}, "tagGenre": {"genre", "tcon", "tco"}, "tagSeries": {"series", "show", "mvnm"}, "tagSeriesPart": {"series-part", "episode_id", "mvin", "part"}, "tagGrouping": {"grouping", "grp1"}, "tagIsbn": {"isbn"}, "tagLanguage": {"language", "lang"}, "tagASIN": {"asin", "audible_asin"}, "tagItunesId": {"itunes-id"}, "tagPodcastType": {"podcast-type"}, "tagEpisodeType": {"episode-type"}, "tagOriginalYear": {"originalyear"}, "tagReleaseCountry": {"musicbrainz album release country", "releasecountry"}, "tagReleaseStatus": {"musicbrainz album status", "releasestatus", "musicbrainz_albumstatus"}, "tagReleaseType": {"musicbrainz album type", "releasetype", "musicbrainz_albumtype"}, "tagISRC": {"tsrc", "isrc"}, "tagMusicBrainzTrackId": {"musicbrainz release track id", "musicbrainz_releasetrackid"}, "tagMusicBrainzAlbumId": {"musicbrainz album id", "musicbrainz_albumid"}, "tagMusicBrainzAlbumArtistId": {"musicbrainz album artist id", "musicbrainz_albumartistid"}, "tagMusicBrainzArtistId": {"musicbrainz artist id", "musicbrainz_artistid"}}

func parseAudioTags(raw map[string]string) map[string]any {
	out := map[string]any{}
	for target, aliases := range audioTagAliases {
		for key, value := range raw {
			for _, alias := range aliases {
				if strings.EqualFold(key, alias) && strings.TrimSpace(value) != "" {
					out[target] = strings.TrimSpace(value)
					goto nextTarget
				}
			}
		}
	nextTarget:
	}
	return out
}

// applyAudioMetadata mirrors AudioFileScanner.setBookMetadataFromAudioMetaTags
// (v2.37.2, AudioFileScanner.js:221-344); the caller supplies only file zero.
func applyAudioMetadata(tags map[string]any, dst *bookMetadata) {
	str := func(keys ...string) string {
		for _, key := range keys {
			if v, ok := tags[key].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
	if v := str("tagComposer"); v != "" {
		dst.Narrators = parseNameList(v)
	}
	if v := str("tagDescription", "tagComment"); v != "" {
		dst.Description = v
	}
	if v := str("tagPublisher"); v != "" {
		dst.Publisher = v
	}
	if v := str("tagDate"); v != "" {
		dst.PublishedYear = v
	}
	if v := str("tagSubtitle"); v != "" {
		dst.Subtitle = v
	}
	if v := str("tagAlbum", "tagTitle"); v != "" {
		dst.Title = v
	}
	if v := str("tagArtist", "tagAlbumArtist"); v != "" {
		dst.Authors = parseNameList(v)
	}
	if v := str("tagGenre"); v != "" {
		dst.Genres = parseGenreList(v)
	}
	if v := str("tagSeries", "tagGrouping"); v != "" {
		if strings.Contains(v, "#") || strings.Contains(v, ";") {
			for _, part := range strings.Split(v, ";") {
				name, seq := parseSeriesValue(strings.TrimSpace(part))
				if name != "" {
					dst.Series = append(dst.Series, seriesMetadata{Name: name, Sequence: seq})
				}
			}
		} else {
			dst.Series = []seriesMetadata{{Name: v, Sequence: str("tagSeriesPart")}}
		}
	}
	if v := str("tagIsbn"); v != "" {
		dst.ISBN = v
	}
	if v := str("tagLanguage"); v != "" {
		dst.Language = v
	}
	if v := str("tagASIN"); v != "" {
		dst.ASIN = v
	}
}
func audioTagsFromFile(path string) map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_format", "-of", "json", path)
	raw, err := cmd.Output()
	if err != nil {
		return nil
	}
	var result struct {
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
	}
	if json.Unmarshal(raw, &result) != nil {
		return nil
	}
	return parseAudioTags(result.Format.Tags)
}

func parseNameList(s string) []string { return splitMetadataList(s) }
func parseGenreList(s string) []string {
	return splitMetadataList(strings.NewReplacer("/", ";").Replace(s))
}
func splitMetadataList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ";") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
func parseSeriesValue(s string) (string, string) {
	if i := strings.LastIndex(s, " #"); i > 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+2:])
	}
	return s, ""
}
func mustJSON(v any) string      { b, _ := json.Marshal(v); return string(b) }
func sameBytes(a, b []byte) bool { return string(a) == string(b) }
func lastFirst(s string) string {
	p := strings.Fields(s)
	if len(p) < 2 {
		return s
	}
	return strings.Join(p[1:], " ") + ", " + p[0]
}
func bookPathMetadata(p, root string, parseSubtitle ...bool) (title, author, series, seq string) {
	rel, _ := filepath.Rel(root, p)
	parts := strings.Split(filepath.ToSlash(rel), "/")
	titleDir := filepath.Base(p)
	seriesName := ""
	if len(parts) > 2 {
		// Upstream getBookDataFromDir (scandir.js:153): after the title folder
		// is popped, a series folder only exists when more than one directory
		// remains, so two-component paths have an author but no series.
		seriesName = parts[len(parts)-2]
	}
	if len(parts) > 2 {
		// Three-deep paths: /Author/Series/Title/ per upstream getBookDataFromDir.
		author = parts[len(parts)-3]
	} else if len(parts) > 1 {
		author = parts[len(parts)-2]
	}
	title = titleDir
	title, _ = extractASIN(title)
	if i := strings.LastIndex(title, " {"); i >= 0 && strings.HasSuffix(title, "}") {
		title = title[:i]
	}
	if seriesName != "" {
		title, seq = extractSequence(title)
	}
	if seriesName != "" {
		series = seriesName
	}
	title, _ = extractPublishedYear(title)
	if len(parseSubtitle) > 0 && parseSubtitle[0] {
		parts := strings.Split(title, " - ")
		title = parts[0]
	}
	return
}
func parseSequence(s string) (string, string) { // Common upstream folder forms: "Book 2 - Title", "Title - Vol 2", "2. Title".
	for _, sep := range []string{" - Vol ", " - vol ", " - Book ", " - book "} {
		if i := strings.LastIndex(s, sep); i > 0 {
			n := strings.TrimSpace(s[i+len(sep):])
			if _, e := strconv.Atoi(n); e == nil {
				return strings.TrimSpace(s[:i]), n
			}
		}
	}
	if i := strings.Index(s, ". "); i > 0 {
		n := strings.TrimSpace(s[:i])
		if _, e := strconv.Atoi(n); e == nil {
			return strings.TrimSpace(s[i+2:]), n
		}
	}
	return s, ""
}

func extractPublishedYear(s string) (string, string) {
	re := regexp.MustCompile(`^ *\(?([0-9]{4})\)? * - *(.+)`)
	m := re.FindStringSubmatch(s)
	if m == nil {
		return s, ""
	}
	return m[2], m[1]
}

func extractASIN(s string) (string, string) {
	pattern := regexp.MustCompile(`(?: |^)\[([A-Z0-9]{10})\](?: |$)`)
	m := pattern.FindStringSubmatchIndex(s)
	if m == nil {
		return s, ""
	}
	asin := s[m[2]:m[3]]
	return strings.TrimSpace(s[:m[0]] + s[m[1]:]), asin
}
func extractSequence(s string) (string, string) {
	re := regexp.MustCompile(`(?i)^(?:(?:vol\.? |volume |book ))?(\d{0,3}(?:\.\d{1,2})?)(\.?)(?: (.*))?$`)
	parts := strings.Split(s, " - ")
	for i, p := range parts {
		m := re.FindStringSubmatch(p)
		if m == nil {
			continue
		}
		labeled := strings.HasPrefix(strings.ToLower(p), "vol") || strings.HasPrefix(strings.ToLower(p), "volume") || strings.HasPrefix(strings.ToLower(p), "book ")
		if m[3] != "" && !labeled && m[2] == "" {
			continue
		}
		n := m[1]
		if n != "" {
			if f, e := strconv.ParseFloat(n, 64); e == nil {
				n = strconv.FormatFloat(f, 'f', -1, 64)
			}
		}
		if m[3] != "" {
			parts[i] = m[3]
		} else {
			parts = append(parts[:i], parts[i+1:]...)
		}
		return strings.Join(parts, " - "), n
	}
	return s, ""
}
