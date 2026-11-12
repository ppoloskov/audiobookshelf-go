package scan

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/text"
	"audiobookshelf-compat/internal/version"
)

// outcome is what importing one item did.
type outcome struct {
	id             string
	added, updated bool
	links          item.ScanLinks
}

// plan is everything known about one item before it is written.
type plan struct {
	lib   library
	c     *candidate
	old   *db.LibraryItem // nil: a new item
	force bool
	now   db.Time

	itemID, mediaID, rel string
	diff                 fileDiff
	book                 db.Book             // the stored book of an existing item
	episodes             []db.PodcastEpisode // the stored episodes of an existing podcast
	audio                []db.AudioFile      // probed or kept entries
	md                   bookMeta
	title, author        string
	series, sequence     string
	authors              []string

	ino                     string
	mtime, ctime, birthtime db.Time
	duration                float64
	size                    int64
	chapters                []db.Chapter
	cover                   string

	skip         bool // nothing to store: a new book without audio, or no inode
	rescanMedia  bool // the media row is rewritten and its links redone
	writeItem    bool // the libraryItems row of an existing item is rewritten
	itemChanged  bool
	coverChanged bool
	missing      bool
	updated      bool
}

// importItem stores what one candidate folder holds: it gathers and plans,
// writes in one transaction and announces. old is the stored item it rescans.
func (s *Scanner) importItem(ctx context.Context, lib library, c *candidate, old *db.LibraryItem, force bool) (outcome, error) {
	p, err := s.gather(ctx, lib, c, old, force)
	if err != nil || p.skip {
		return outcome{}, err
	}
	out := outcome{id: p.itemID, added: old == nil, updated: p.updated}
	var removal item.EpisodeRemoval
	err = s.db.Tx(ctx, func(ctx context.Context) error {
		var err error
		if lib.mediaType == "podcast" {
			removal, err = s.savePodcast(ctx, p)
		} else {
			out.links, err = s.saveBook(ctx, p)
		}
		if err != nil {
			return err
		}
		return s.saveItem(ctx, p)
	})
	if err != nil {
		return outcome{}, err
	}
	removal.Announce(ctx, s.items)
	if lib.mediaType == "book" && (old == nil || (p.rescanMedia && (p.diff.changed() || p.coverChanged))) {
		if err := s.WriteMetadata(ctx, p.itemID); err != nil {
			slog.Warn("writing metadata.json failed", "item", p.itemID, "error", err)
		}
	}
	out.links.Announce(s.items)
	return out, nil
}

// gather reads the disk and the stored rows into a plan.
func (s *Scanner) gather(ctx context.Context, lib library, c *candidate, old *db.LibraryItem, force bool) (*plan, error) {
	p := &plan{lib: lib, c: c, old: old, force: force, now: db.Now(), itemID: ids.Must(), mediaID: ids.Must()}
	rel, _ := filepath.Rel(db.Val(c.folder.Path), c.path)
	p.rel = filepath.ToSlash(rel)
	podcast := lib.mediaType == "podcast"
	var stored []db.LibraryFile
	var prior []db.AudioFile
	if old != nil {
		p.itemID = old.ID
		if id := db.Val(old.MediaID); id != "" {
			p.mediaID = id
		}
		stored = old.LibraryFiles.V
		var err error
		if podcast {
			err = s.db.Select(ctx, &p.episodes, `SELECT * FROM podcastEpisodes WHERE podcastId = ? ORDER BY "index"`, p.mediaID)
			for _, ep := range p.episodes {
				prior = append(prior, ep.AudioFile.V)
			}
		} else {
			if err = s.db.Get(ctx, &p.book, `SELECT * FROM books WHERE id = ?`, p.mediaID); errors.Is(err, sql.ErrNoRows) {
				err = nil
			}
			prior = p.book.AudioFiles.V
		}
		if err != nil {
			return nil, err
		}
	}
	p.diff = diffFiles(stored, c.files(), p.now.Millis())
	if err := s.probe(ctx, p, prior); err != nil {
		return nil, err
	}
	if podcast {
		p.title = filepath.Base(c.path)
	} else {
		s.resolve(p)
	}
	newest := time.Time{}
	for _, f := range c.files() {
		if f.info.ModTime().After(newest) {
			newest = f.info.ModTime()
		}
	}
	p.mtime, p.ctime, p.birthtime = db.Time{Time: newest.UTC()}, db.Time{Time: newest.UTC()}, db.Time{Time: newest.UTC()}
	if c.info != nil {
		mtime, ctime, birthtime := statTimes(c.path, c.info)
		p.ino, p.mtime, p.ctime, p.birthtime = inode(c.info), db.Time{Time: mtime.UTC()}, db.Time{Time: ctime.UTC()}, db.Time{Time: birthtime.UTC()}
	}
	if p.ino == "" && len(c.media) > 0 {
		p.ino = inode(c.media[0].info)
	}
	s.decide(p)
	return p, nil
}

// probe builds the audio entries: an unchanged file keeps its stored entry, a
// rescanned one keeps addedAt, updatedAt and manuallyVerified of the old entry,
// and a file without an audio stream is dropped.
func (s *Scanner) probe(ctx context.Context, p *plan, prior []db.AudioFile) error {
	want := make([]keyed, len(p.c.media))
	for i, f := range p.c.media {
		want[i] = keyed{f.path, []string{inode(f.info)}}
	}
	have := make([]keyed, len(prior))
	for i, a := range prior {
		have[i] = keyed{a.Metadata.Path, []string{a.Ino}}
	}
	paired := pair(want, have)
	touched := p.diff.touched()
	p.audio = make([]db.AudioFile, 0, len(p.c.media))
	for i, f := range p.c.media {
		if err := ctx.Err(); err != nil {
			return err
		}
		var before *db.AudioFile
		if paired[i] >= 0 {
			before = &prior[paired[i]]
			if !touched[f.path] {
				p.audio = append(p.audio, *before)
				continue
			}
		}
		entry, err := probeAudio(ctx, newLibraryFile(f))
		if err != nil {
			slog.Warn("skipping file: ffprobe failed", "path", f.path, "error", err)
			continue
		}
		if entry == nil {
			continue
		}
		if before != nil {
			entry.AddedAt, entry.UpdatedAt, entry.ManuallyVerified = before.AddedAt, before.UpdatedAt, before.ManuallyVerified
		}
		p.audio = append(p.audio, *entry)
	}
	return nil
}

// resolve merges the metadata sources by the library's precedence. Folder
// names seed it, wherever "folderStructure" stands in the list.
func (s *Scanner) resolve(p *plan) {
	c, lib := p.c, p.lib
	title, author, series, sequence := folderMeta(c.path, db.Val(c.folder.Path), c.isFile, lib.parseSubtitle)
	m := bookMeta{Title: title, Authors: splitList(author)}
	if series != "" {
		m.Series = []SeriesEntry{{series, sequence}}
	}
	var tags db.AudioMetaTags
	if len(p.audio) > 0 {
		tags = p.audio[0].MetaTags
	}
	fallback := ""
	if p.old != nil && s.cfg.MetadataPath != "" {
		fallback = filepath.Join(s.cfg.Dirs().Items, p.itemID, "metadata.json")
	}
	for _, source := range lib.precedence {
		if source == "audioMetatags" {
			if len(c.media) > 0 {
				m.applyTags(tags)
			}
			continue
		}
		readSidecars(source, c.other, fallback, &m)
	}
	p.md = m
	p.title, p.author, p.series, p.sequence = cmpOrString(m.Title, title), author, series, sequence
	if len(m.Authors) > 0 {
		p.author = strings.Join(m.Authors, ", ")
	}
	if len(m.Series) > 0 {
		p.series, p.sequence = m.Series[0].Name, m.Series[0].Sequence
	}
	p.authors = append([]string(nil), m.Authors...)
	if len(p.authors) == 0 && p.author != "" {
		p.authors = []string{p.author}
	}
}

// decide derives what to write. Media is rescanned only for forced scans,
// library file changes or a path change (LibraryScanner.js:200).
func (s *Scanner) decide(p *plan) {
	lib, old := p.lib, p.old
	podcast := lib.mediaType == "podcast"
	if !podcast {
		if old == nil && len(p.audio) == 0 {
			p.skip = true
			return
		}
		orderAudio(p.audio)
	}
	if p.ino == "" {
		p.skip = true
		return
	}
	for _, a := range p.audio {
		p.duration += db.Val(a.Duration)
	}
	p.chapters = cmpOr(p.md.Chapters, buildChapters(p.audio, p.title))
	for _, f := range p.diff.files {
		p.size += f.Metadata.Size
	}
	if !podcast {
		p.cover, p.coverChanged = coverPath(db.Val(p.book.CoverPath), p.diff)
	}
	if old == nil {
		p.rescanMedia = true
		return
	}
	pathChanged := db.Val(old.Path) != p.c.path || db.Val(old.RelPath) != p.rel
	p.rescanMedia = p.force || p.diff.changed() || pathChanged
	statChanged := old.Mtime.Millis() != p.mtime.Millis() || old.Ctime.Millis() != p.ctime.Millis() || old.Birthtime.Millis() != p.birthtime.Millis()
	p.itemChanged = p.diff.changed() || statChanged || pathChanged || db.Val(old.Ino) != p.ino || db.Val(old.MediaType) != lib.mediaType || bool(old.IsMissing)
	if !podcast {
		// A book with neither audio nor an ebook file is missing (BookScanner.js:405-417).
		p.missing = len(p.audio) == 0 && p.book.EbookFile.V == nil
		p.itemChanged = p.itemChanged || (p.missing && !bool(old.IsMissing))
	}
	p.writeItem = p.itemChanged || p.force
	p.updated = p.itemChanged || p.coverChanged
}

// nullable is nil for a blank string.
func nullable(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// keep sets *dst to s unless s is blank: a rescan never erases a value.
func keep(dst **string, s string) {
	if v := nullable(s); v != nil {
		*dst = v
	}
}

func orEmpty(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

// saveBook inserts or rewrites the book row and relinks its authors and series.
func (s *Scanner) saveBook(ctx context.Context, p *plan) (item.ScanLinks, error) {
	var links item.ScanLinks
	added, md := p.old == nil, p.md
	if !added && !p.rescanMedia {
		return links, nil
	}
	b := &p.book
	if added {
		*b = db.Book{ID: p.mediaID, CreatedAt: p.now}
	}
	keep(&b.Title, p.title)
	b.TitleIgnorePrefix = db.Ptr(text.TitleIgnorePrefix(p.title, p.lib.prefixes))
	keep(&b.Subtitle, md.Subtitle)
	keep(&b.PublishedYear, md.PublishedYear)
	keep(&b.PublishedDate, md.PublishedDate)
	keep(&b.Publisher, md.Publisher)
	keep(&b.Description, md.Description)
	keep(&b.ISBN, md.ISBN)
	keep(&b.ASIN, md.ASIN)
	keep(&b.Language, md.Language)
	b.Duration, b.AudioFiles.V, b.Chapters.V, b.CoverPath, b.UpdatedAt = &p.duration, p.audio, p.chapters, nullable(p.cover), p.now
	if added || len(md.Tags) > 0 {
		b.Tags.V = orEmpty(md.Tags)
	}
	if added || len(md.Genres) > 0 {
		b.Genres.V = orEmpty(md.Genres)
	}
	if added || len(md.Narrators) > 0 {
		b.Narrators.V = orEmpty(md.Narrators)
	}
	var err error
	if added {
		err = s.db.Insert(ctx, "books", b)
	} else {
		err = s.db.Update(ctx, "books", b, "title", "titleIgnorePrefix", "subtitle", "publishedYear", "publishedDate", "publisher", "description",
			"isbn", "asin", "language", "duration", "audioFiles", "chapters", "coverPath", "tags", "genres", "narrators", "updatedAt")
	}
	if err != nil {
		return links, err
	}
	return s.items.LinkScanned(ctx, item.ScannedBook{LibraryID: p.lib.id, BookID: p.mediaID, Authors: p.authors, Series: p.series, Sequence: p.sequence})
}

// savePodcast inserts the podcast, drops the episodes whose file left the
// folder and adds one episode per audio file that has none.
func (s *Scanner) savePodcast(ctx context.Context, p *plan) (item.EpisodeRemoval, error) {
	var removal item.EpisodeRemoval
	if p.old == nil {
		row := db.Podcast{ID: p.mediaID, Title: &p.title, PodcastType: db.Ptr("episodic"), AutoDownloadSchedule: db.Ptr("0 * * * *"), LastEpisodeCheck: p.now,
			MaxEpisodesToKeep: db.Ptr(int64(0)), MaxNewEpisodesToDownload: db.Ptr(int64(3)), Tags: db.JSON[[]string]{V: []string{}}, Genres: db.JSON[[]string]{V: []string{}},
			CreatedAt: p.now, UpdatedAt: p.now}
		if err := s.db.Insert(ctx, "podcasts", &row); err != nil {
			return removal, err
		}
	} else {
		var row db.Podcast
		if err := s.db.Get(ctx, &row, `SELECT * FROM podcasts WHERE id = ?`, p.mediaID); err == nil && db.Val(row.Title) == "" {
			row.Title, row.UpdatedAt = &p.title, p.now
			if err := s.db.Update(ctx, "podcasts", &row, "title", "updatedAt"); err != nil {
				return removal, err
			}
		}
	}
	var gone []string
	titles := map[string]bool{}
	for _, ep := range p.episodes {
		a := ep.AudioFile.V
		if slicesContainsRemoved(p.diff.removed, a.Metadata.Path, a.Ino) {
			gone = append(gone, ep.ID)
		} else {
			titles[db.Val(ep.Title)] = true
		}
	}
	removal, err := s.items.DeleteEpisodes(ctx, gone)
	if err != nil {
		return removal, err
	}
	count := len(p.episodes) - len(gone)
	for i, af := range p.audio {
		name := strings.TrimSuffix(af.Metadata.Filename, filepath.Ext(af.Metadata.Filename))
		if titles[name] {
			continue
		}
		titles[name] = true
		n := int64(count + i + 1)
		ep := db.PodcastEpisode{ID: ids.Must(), PodcastID: &p.mediaID, Index: &n, Episode: db.Ptr(strconv.FormatInt(n, 10)), EpisodeType: db.Ptr("full"), Title: &name,
			AudioFile: db.JSON[db.AudioFile]{V: af}, Chapters: db.JSON[[]db.Chapter]{V: []db.Chapter{}}, ExtraData: db.JSON[*db.EpisodeExtra]{V: &db.EpisodeExtra{}}, CreatedAt: p.now, UpdatedAt: p.now}
		if err := s.db.Insert(ctx, "podcastEpisodes", &ep); err != nil {
			return removal, err
		}
	}
	return removal, nil
}

// slicesContainsRemoved reports whether a removed library file is the episode's
// file, by path and then inode.
func slicesContainsRemoved(removed []db.LibraryFile, path, ino string) bool {
	for _, f := range removed {
		if f.Metadata.Path == path || (ino != "" && f.Ino == ino) {
			return true
		}
	}
	return false
}

// saveItem inserts the libraryItems row or, when something changed, rewrites
// it. A scan that changed nothing leaves the row alone. Title and author
// names follow the media row and link triggers.
func (s *Scanner) saveItem(ctx context.Context, p *plan) error {
	lib, c := p.lib, p.c
	if p.old == nil {
		row := db.LibraryItem{ID: p.itemID, Ino: &p.ino, Path: &c.path, RelPath: &p.rel, MediaID: &p.mediaID, MediaType: &lib.mediaType, IsFile: db.Bool(c.isFile),
			Mtime: p.mtime, Ctime: p.ctime, Birthtime: p.birthtime, Size: &p.size, LastScan: p.now, LastScanVersion: db.Ptr(version.Server),
			LibraryFiles: db.JSON[[]db.LibraryFile]{V: p.diff.files}, ExtraData: db.JSON[*db.LibraryItemExtra]{V: &db.LibraryItemExtra{}},
			Title: &p.title, TitleIgnorePrefix: db.Ptr(text.TitleIgnorePrefix(p.title, lib.prefixes)),
			AuthorNamesFirstLast: &p.author, AuthorNamesLastFirst: db.Ptr(text.ToLastFirst(p.author)),
			CreatedAt: p.now, UpdatedAt: p.now, LibraryID: &lib.id, LibraryFolderID: &c.folder.ID}
		return s.db.Insert(ctx, "libraryItems", &row)
	}
	if !p.writeItem {
		return nil
	}
	row := p.old
	row.Path, row.Ino, row.LibraryFolderID, row.RelPath, row.IsFile = &c.path, &p.ino, &c.folder.ID, &p.rel, db.Bool(c.isFile)
	row.Mtime, row.Ctime, row.Birthtime, row.UpdatedAt, row.LastScan = p.mtime, p.ctime, p.birthtime, p.now, p.now
	row.LastScanVersion, row.IsMissing, row.Size, row.LibraryFiles.V = db.Ptr(version.Server), db.Bool(p.missing), &p.size, p.diff.files
	if err := s.db.Update(ctx, "libraryItems", row, "path", "ino", "libraryFolderId", "relPath", "isFile", "mtime", "ctime", "birthtime", "updatedAt",
		"lastScan", "lastScanVersion", "isMissing", "size", "libraryFiles"); err != nil {
		return fmt.Errorf("update item %s: %w", p.itemID, err)
	}
	return nil
}
