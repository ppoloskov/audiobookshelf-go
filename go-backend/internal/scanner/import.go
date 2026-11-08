package scanner

import (
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/nameparse"
	"audiobookshelf-compat/internal/playlistview"
	"audiobookshelf-compat/internal/textsort"
	"audiobookshelf-compat/internal/version"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/itemwrite"
	"audiobookshelf-compat/internal/store"
)

func (s *Scanner) importGroup(ctx context.Context, l library, g *group, force bool) (imported, error) {
	in, err := s.gather(ctx, l, g, force)
	if err != nil {
		return imported{}, err
	}
	p := plan(in)
	if p.skip {
		return imported{}, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return imported{}, err
	}
	defer tx.Rollback()
	var changes itemwrite.Changes
	tally := imported{id: p.itemID, added: p.added, updated: p.updated, scannedAudio: in.scannedNew}
	var playlistChanges []store.PlaylistChange
	if p.lib.mediaType == "podcast" {
		playlistChanges, err = p.applyPodcast(ctx, tx)
	} else if err = p.applyBook(ctx, tx); err == nil && p.rescanMedia {
		err = p.applyRelations(ctx, tx, &changes, &tally)
	}
	if err == nil {
		err = p.applyItem(ctx, tx)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return imported{}, err
	}
	playlistview.Announce(ctx, s.db, s.emit, playlistChanges)
	s.afterImport(ctx, p, &changes)
	return tally, nil
}

type planInput struct {
	lib     library
	g       *group
	force   bool
	old     *existingItem // nil: a new item
	itemID  string
	mediaID string
	rel     string
	now     string // DB-format time of this import

	fileDiff                   libraryFileDiff
	audio                      []libitem.AudioFile // probed or kept entries, in file order
	scannedNew                 []string            // audio paths new to an existing book
	md                         bookMetadata
	title, author, series, seq string

	itemIno                 string // inode of the item (directory or file); "" when unknown
	mtime, ctime, birthtime string // item times from stat, or the newest file time
	oldCover                string
	hasEbook                bool
}

// episodePlan is a podcast episode to insert unless one with the title exists.
type episodePlan struct {
	filename, title string
	audioJSON       string
	offset          int // position among the scanned audio files
}

type itemPlan struct {
	planInput
	skip    bool // nothing to store (no audio in a new book, or no inode)
	added   bool
	updated bool // the import result's "updated" flag

	chapters     any // []libitem.AudioChapter, or the sidecar's chapters verbatim
	authors      []string
	duration     float64
	size         int64
	filesJSON    string
	coverPath    string
	coverChanged bool
	rescanMedia  bool // the media row is rewritten and relations re-linked
	writeItem    bool // an existing libraryItems row is updated
	itemChanged  bool
	missing      bool
	episodes     []episodePlan
}

// plan is pure: the same input always yields the same plan.
func plan(in planInput) itemPlan {
	l := in.lib
	podcast := l.mediaType == "podcast"
	added := in.old == nil
	p := itemPlan{planInput: in, added: added}
	audio := in.audio
	if !podcast {
		if added && len(audio) == 0 {
			p.skip = true
			return p
		}
		smartAudioOrder(audio)
	}
	for _, af := range audio {
		p.duration += af.Duration
	}
	p.audio = audio
	p.chapters = buildChapters(audio, in.title)
	if len(in.md.Chapters) > 0 {
		p.chapters = in.md.Chapters
	}
	if in.itemIno == "" {
		p.skip = true
		return p
	}
	if p.mtime == "" {
		p.mtime, p.ctime, p.birthtime = in.now, in.now, in.now
	}
	p.authors = append([]string(nil), in.md.Authors...)
	if len(p.authors) == 0 && in.author != "" {
		p.authors = []string{in.author}
	}
	if podcast {
		p.title, p.author, p.series = filepath.Base(in.g.path), "", ""
		p.episodes = planEpisodes(audio)
	}
	filesMeta := in.fileDiff.files
	p.size = libraryFileSize(filesMeta)
	fj, _ := json.Marshal(filesMeta)
	p.filesJSON = string(fj)

	// Upstream rescans media only for forced scans, library file changes or a
	// path change (LibraryScanner.js:200).
	o := in.old
	pathChanged := !added && (o.path != in.g.path || o.relPath != in.rel)
	p.rescanMedia = added || in.force || in.fileDiff.changed() || pathChanged
	statChanged := !added && !(sameDBTime(o.mtime, p.mtime) && sameDBTime(o.ctime, p.ctime) && sameDBTime(o.birthtime, p.birthtime))
	p.itemChanged = !added && (in.fileDiff.changed() || statChanged || pathChanged || o.ino != in.itemIno || o.mediaType != l.mediaType || o.missing)
	if !podcast {
		p.coverPath, p.coverChanged = nextCoverPath(in.oldCover, filesMeta, in.fileDiff)
		// A book with neither audio nor an ebook file is missing (BookScanner.js:405-417).
		p.missing = !added && len(audio) == 0 && !in.hasEbook
		if p.missing && !o.missing {
			p.itemChanged = true
		}
	}
	p.writeItem = p.itemChanged || in.force
	p.updated = p.itemChanged || (!added && p.coverChanged)
	return p
}

func planEpisodes(audio []libitem.AudioFile) []episodePlan {
	out := make([]episodePlan, 0, len(audio))
	for i, af := range audio {
		filename := audioFilename(af)
		raw, _ := json.Marshal(af)
		out = append(out, episodePlan{filename: filename, title: filename, audioJSON: string(raw), offset: i})
	}
	return out
}

func (s *Scanner) gather(ctx context.Context, l library, g *group, force bool) (planInput, error) {
	in := planInput{lib: l, g: g, force: force, old: g.old, now: dbtime.Now()}
	added := g.old == nil
	in.itemID, in.mediaID = ids.Must(), ids.Must()
	var stored []libraryFile
	var oldAudio []libitem.AudioFile
	var oldID string
	if !added {
		o := g.old
		oldID, in.itemID = o.id, o.id
		if o.mediaID != "" {
			in.mediaID = o.mediaID
		}
		stored = parseStoredLibraryFiles(o.files)
		oldAudio = s.loadStoredAudio(ctx, o.mediaID)
	}
	rel, _ := filepath.Rel(g.folder.path, g.path)
	in.rel = filepath.ToSlash(rel)
	related := append(append([]file(nil), g.files...), g.companions...)
	in.fileDiff = mergeLibraryFiles(stored, related, g.path, time.Now().UnixMilli())
	if err := s.probeFiles(ctx, &in, oldAudio); err != nil {
		return in, err
	}
	s.resolveMetadata(&in, oldID)
	newest := time.Time{}
	for _, f := range related {
		if f.info.ModTime().After(newest) {
			newest = f.info.ModTime()
		}
	}
	in.mtime, in.ctime, in.birthtime = timeString(newest), timeString(newest), timeString(newest)
	if info, err := os.Stat(g.path); err == nil {
		mt, ct, bt := statTimes(g.path, info)
		in.itemIno, in.mtime, in.ctime, in.birthtime = inode(info), timeString(mt), timeString(ct), timeString(bt)
	}
	if l.mediaType != "podcast" && !added {
		var oldCover, ebook sql.NullString
		_ = s.db.QueryRowContext(ctx, `SELECT coverPath,ebookFile FROM books WHERE id=?`, in.mediaID).Scan(&oldCover, &ebook)
		in.oldCover = oldCover.String
		in.hasEbook = ebook.Valid && ebook.String != "" && ebook.String != "null"
	}
	return in, nil
}

func (s *Scanner) resolveMetadata(in *planInput, oldID string) {
	g, l := in.g, in.lib
	title, author, series, seq := bookPathMetadata(g.path, g.folder.path, l.parseSubtitle)
	if g.isFile {
		title = strings.TrimSuffix(filepath.Base(g.path), filepath.Ext(g.path))
	}
	if l.mediaType != "podcast" {
		var firstTags map[string]string
		if len(in.audio) > 0 {
			firstTags = in.audio[0].MetaTags
		}
		abs, hasABS := readABSMetadata(g, oldID, s.cfg)
		in.md = mergeBookMetadataWithABS(title, author, series, seq, g, l.metadataPrecedence, firstTags, abs, hasABS)
	}
	if in.md.Title != "" {
		title = in.md.Title
	}
	if len(in.md.Authors) > 0 {
		author = strings.Join(in.md.Authors, ", ")
	}
	if len(in.md.Series) > 0 {
		series, seq = in.md.Series[0].Name, in.md.Series[0].Sequence
	}
	in.title, in.author, in.series, in.seq = title, author, series, seq
}

// probeFiles builds the audio entries: unchanged files keep their stored
// entry untouched, as BookScanner.rescanExistingBookLibraryItem only rescans
// added or modified files (BookScanner.js:68-96); the rest are probed.
func (s *Scanner) probeFiles(ctx context.Context, in *planInput, oldAudio []libitem.AudioFile) error {
	g, added := in.g, in.old == nil
	touched := in.fileDiff.touchedPaths()
	in.audio = make([]libitem.AudioFile, 0, len(g.files))
	for _, f := range g.files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if in.itemIno == "" {
			in.itemIno = inode(f.info)
		}
		var prior *libitem.AudioFile
		if !added {
			prior = findStoredAudio(oldAudio, f.path, inode(f.info))
			if !touched[f.path] && prior != nil {
				in.audio = append(in.audio, *prior)
				continue
			}
		}
		lf := newLibraryFile(g.path, f)
		af, err := probeAudio(ctx, f.path, lf.Metadata, lf.Ino)
		if err != nil {
			logging.Warnf("[Scanner] Skipping %q: ffprobe failed: %v", f.path, err)
			continue
		}
		if af == nil {
			continue
		}
		if prior != nil {
			// AudioFile.updateFromScan keeps the established entry identity
			// (AudioFile.js:171-196); scanned data replaces probe fields.
			af.AddedAt, af.UpdatedAt, af.ManuallyVerified = prior.AddedAt, prior.UpdatedAt, prior.ManuallyVerified
		} else if !added {
			in.scannedNew = append(in.scannedNew, f.path)
		}
		in.audio = append(in.audio, *af)
	}
	return nil
}

func (p itemPlan) applyPodcast(ctx context.Context, tx *sql.Tx) ([]store.PlaylistChange, error) {
	if p.added {
		if _, err := tx.ExecContext(ctx, `INSERT INTO podcasts(id,title,author,releaseDate,feedURL,imageURL,description,itunesPageURL,itunesId,itunesArtistId,language,podcastType,explicit,autoDownloadEpisodes,autoDownloadSchedule,lastEpisodeCheck,maxEpisodesToKeep,maxNewEpisodesToDownload,coverPath,tags,genres,createdAt,updatedAt) VALUES(?,?,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,'episodic',0,0,'0 * * * *',?,0,3,NULL,'[]','[]',?,?)`, p.mediaID, p.title, p.now, p.now, p.now); err != nil {
			return nil, err
		}
	} else if _, err := tx.ExecContext(ctx, `UPDATE podcasts SET title=CASE WHEN title IS NULL OR title='' THEN ? ELSE title END,updatedAt=? WHERE id=?`, p.title, p.now, p.mediaID); err != nil {
		return nil, err
	}
	changes, err := p.removeEpisodes(ctx, tx)
	if err != nil {
		return nil, err
	}
	// A podcast directory is the library item; each audio file is an episode.
	var count int
	_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM podcastEpisodes WHERE podcastId=?`, p.mediaID).Scan(&count)
	for _, ep := range p.episodes {
		var existing string
		_ = tx.QueryRowContext(ctx, `SELECT id FROM podcastEpisodes WHERE podcastId=? AND title=? ORDER BY "index" LIMIT 1`, p.mediaID, ep.filename).Scan(&existing)
		if existing != "" {
			continue
		}
		n := count + ep.offset + 1
		if _, err := tx.ExecContext(ctx, `INSERT INTO podcastEpisodes(id,podcastId,"index",season,episode,episodeType,title,subtitle,description,enclosureURL,enclosureSize,enclosureType,pubDate,audioFile,chapters,extraData,publishedAt,createdAt,updatedAt) VALUES(?,?,?,NULL,?,'full',?,NULL,NULL,NULL,NULL,NULL,NULL,?,'[]','{}',NULL,?,?)`, ids.Must(), p.mediaID, n, strconv.Itoa(n), ep.title, ep.audioJSON, p.now, p.now); err != nil {
			return nil, err
		}
	}
	return changes, nil
}

// removeEpisodes drops the episodes whose audio file left the folder, matched
// by path and then inode (PodcastScanner.js:66-102,
// LibraryItemScanData.checkAudioFileRemoved), with their playlist entries and
// progress.
func (p itemPlan) removeEpisodes(ctx context.Context, tx *sql.Tx) ([]store.PlaylistChange, error) {
	if len(p.fileDiff.removed) == 0 {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,COALESCE(json_extract(audioFile,'$.metadata.path'),''),COALESCE(json_extract(audioFile,'$.ino'),'') FROM podcastEpisodes WHERE podcastId=?`, p.mediaID)
	if err != nil {
		return nil, err
	}
	var gone []string
	for rows.Next() {
		var id, path, ino string
		if err = rows.Scan(&id, &path, &ino); err != nil {
			rows.Close()
			return nil, err
		}
		for _, f := range p.fileDiff.removed {
			if f.Metadata.Path == path || (ino != "" && f.Ino == ino) {
				gone = append(gone, id)
				break
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return store.DeleteEpisodes(ctx, tx, gone)
}

func (p itemPlan) applyBook(ctx context.Context, tx *sql.Tx) error {
	l := p.lib
	md := p.md
	if p.added {
		_, err := tx.ExecContext(ctx, `INSERT INTO books(id,title,titleIgnorePrefix,subtitle,publishedYear,publishedDate,publisher,description,isbn,asin,language,explicit,abridged,coverPath,duration,narrators,audioFiles,ebookFile,chapters,tags,genres,createdAt,updatedAt) VALUES(?,?,?,?,?,?,?,?,?,?,?,0,0,?,?,?,?,NULL,?,?,?, ?,?)`, p.mediaID, p.title, textsort.TitleIgnorePrefix(p.title, l.sortingPrefixes), store.NullIfBlank(md.Subtitle), store.NullIfBlank(md.PublishedYear), store.NullIfBlank(md.PublishedDate), store.NullIfBlank(md.Publisher), store.NullIfBlank(md.Description), store.NullIfBlank(md.ISBN), store.NullIfBlank(md.ASIN), store.NullIfBlank(md.Language), store.NullIfBlank(p.coverPath), p.duration, mustJSON(md.Narrators), mustJSON(p.audio), mustJSON(p.chapters), mustJSON(md.Tags), mustJSON(md.Genres), p.now, p.now)
		return err
	}
	if !p.rescanMedia {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE books SET title=COALESCE(?,title),titleIgnorePrefix=?,subtitle=COALESCE(?,subtitle),publishedYear=COALESCE(?,publishedYear),publishedDate=COALESCE(?,publishedDate),publisher=COALESCE(?,publisher),description=COALESCE(?,description),isbn=COALESCE(?,isbn),asin=COALESCE(?,asin),language=COALESCE(?,language),duration=?,audioFiles=?,chapters=?,coverPath=?,tags=CASE WHEN ?='[]' THEN tags ELSE ? END,genres=CASE WHEN ?='[]' THEN genres ELSE ? END,narrators=CASE WHEN ?='[]' THEN narrators ELSE ? END,updatedAt=? WHERE id=?`, store.NullIfBlank(md.Title), textsort.TitleIgnorePrefix(p.title, l.sortingPrefixes), store.NullIfBlank(md.Subtitle), store.NullIfBlank(md.PublishedYear), store.NullIfBlank(md.PublishedDate), store.NullIfBlank(md.Publisher), store.NullIfBlank(md.Description), store.NullIfBlank(md.ISBN), store.NullIfBlank(md.ASIN), store.NullIfBlank(md.Language), p.duration, mustJSON(p.audio), mustJSON(p.chapters), store.NullIfBlank(p.coverPath), mustJSON(md.Tags), mustJSON(md.Tags), mustJSON(md.Genres), mustJSON(md.Genres), mustJSON(md.Narrators), mustJSON(md.Narrators), p.now, p.mediaID)
	return err
}

// applyRelations links the scanned authors and series; on rescans it drops
// links no longer in the scanned metadata (BookScanner.js:114-143). Names
// match by exact case.
func (p itemPlan) applyRelations(ctx context.Context, tx *sql.Tx, changes *itemwrite.Changes, tally *imported) error {
	l := p.lib
	rules := itemwrite.Rules{Exact: true, Replace: !p.added && len(p.authors) > 0, KeepSequence: true}
	if _, err := changes.LinkAuthors(ctx, tx, l.id, p.mediaID, p.authors, rules); err != nil {
		return err
	}
	if p.series != "" {
		rules.Replace = !p.added
		if _, err := changes.LinkSeries(ctx, tx, l.id, p.mediaID, []itemwrite.Series{{Name: p.series, Sequence: p.seq}}, l.sortingPrefixes, rules); err != nil {
			return err
		}
	}
	// Only a link added to an existing author changes its book count
	// (BookScanner.js:75, 211-219).
	tally.authorsChanged = append(append(tally.authorsChanged, changes.AuthorsRelinked...), changes.AuthorsUnlinked...)
	tally.authorsRemoved = append(tally.authorsRemoved, changes.AuthorsUnlinked...)
	tally.seriesRemoved = append(tally.seriesRemoved, changes.SeriesUnlinked...)
	return nil
}

// applyItem inserts the libraryItems row or, when something changed, updates
// it. A no-op scan must not rewrite item bookkeeping.
func (p itemPlan) applyItem(ctx context.Context, tx *sql.Tx) error {
	l, g := p.lib, p.g
	if p.added {
		_, err := tx.ExecContext(ctx, `INSERT INTO libraryItems(id,ino,libraryId,libraryFolderId,path,relPath,isFile,mtime,ctime,birthtime,createdAt,updatedAt,lastScan,lastScanVersion,isMissing,isInvalid,mediaType,size,extraData,libraryFiles,mediaId,title,titleIgnorePrefix,authorNamesFirstLast,authorNamesLastFirst) VALUES(?,?,?,?,?,?,?, ?,?,?,?,?,?,?,0,0,?,?,?,?,?,?,?, ?,?)`, p.itemID, p.itemIno, l.id, g.folder.id, g.path, p.rel, boolInt(g.isFile), p.mtime, p.ctime, p.birthtime, p.now, p.now, p.now, version.Server, strings.ToLower(l.mediaType), p.size, `{}`, p.filesJSON, p.mediaID, p.title, textsort.TitleIgnorePrefix(p.title, l.sortingPrefixes), p.author, nameparse.ToLastFirst(p.author))
		return err
	}
	if !p.writeItem {
		return nil
	}
	_, err := tx.ExecContext(ctx, `UPDATE libraryItems SET path=?,ino=?,libraryFolderId=?,relPath=?,isFile=?,mtime=?,ctime=?,birthtime=?,updatedAt=?,lastScan=?,lastScanVersion=?,isMissing=?,size=?,libraryFiles=?,title=?,titleIgnorePrefix=?,authorNamesFirstLast=?,authorNamesLastFirst=? WHERE id=?`, g.path, p.itemIno, g.folder.id, p.rel, boolInt(g.isFile), p.mtime, p.ctime, p.birthtime, p.now, p.now, version.Server, boolInt(p.missing), p.size, p.filesJSON, p.title, textsort.TitleIgnorePrefix(p.title, l.sortingPrefixes), p.author, nameparse.ToLastFirst(p.author), p.itemID)
	return err
}

func (s *Scanner) afterImport(ctx context.Context, p itemPlan, changes *itemwrite.Changes) {
	l := p.lib
	if l.mediaType == "book" && (p.added || (p.rescanMedia && (p.fileDiff.changed() || p.coverChanged))) {
		if err := s.WriteMetadata(ctx, p.itemID); err != nil {
			logging.Warnf("[Scanner] Writing metadata for item %s failed: %v", p.itemID, err)
		}
	}
	if p.added && l.mediaType == "book" {
		md := p.md
		values := filterdata.BookValues{Narrators: md.Narrators, Genres: md.Genres, Tags: md.Tags, Publishers: []string{md.Publisher}, Languages: []string{md.Language}}
		if year, e := strconv.Atoi(md.PublishedYear); e == nil && year != 0 {
			values.PublishedDecades = []string{strconv.Itoa(year / 10 * 10)}
		}
		s.filters.AddBook(l.id, values)
	}
	changes.Announce(l.id, s.filters, s.emit)
}
