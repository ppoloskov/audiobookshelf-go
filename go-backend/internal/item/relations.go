package item

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/filterdata"
	idgen "audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/text"
)

// source says who edits a book, which decides the events its links announce.
type source int

const (
	fromRequest source = iota // PATCH media: author_updated for reused and shrunken authors
	fromMatch                 // quick match: series_added for a created series
)

type event struct {
	name    string
	payload any
}

// changes collects what link edits did so the caller can announce it once the
// transaction has committed.
type changes struct {
	events          []event
	authorsAdded    []filterdata.Named
	authorsUnlinked []string
	seriesUnlinked  []string
}

func (c *changes) merge(o changes) {
	c.events = append(c.events, o.events...)
	c.authorsAdded = append(c.authorsAdded, o.authorsAdded...)
	c.authorsUnlinked = append(c.authorsUnlinked, o.authorsUnlinked...)
	c.seriesUnlinked = append(c.seriesUnlinked, o.seriesUnlinked...)
}

// announce sends the collected events (LibraryItemController.js:240-265).
func (c *changes) announce(emit events.Emitter) {
	for _, e := range c.events {
		emit.ToAll(e.name, e.payload)
	}
}

// clock stamps rows inserted one after another. Sequelize stamps every create
// separately and books read their authors and series back ordered by the
// link's createdAt, so each stamp is a millisecond after the one before.
type clock struct{ last time.Time }

func (c *clock) now() db.Time {
	t := time.Now().UTC().Truncate(time.Millisecond)
	if !t.After(c.last) {
		t = c.last.Add(time.Millisecond)
	}
	c.last = t
	return db.Time{Time: t}
}

// link is a book's author or series in link order.
type link struct {
	ID       string  `db:"id"`
	Name     string  `db:"name"`
	Sequence *string `db:"sequence"`
}

// currentLinks reads the book's parents in link order. sequence is the SQL of
// the sequence column ("NULL" for authors).
func (h *Handler) currentLinks(ctx context.Context, table, junction, col, sequence, bookID string) ([]link, error) {
	var links []link
	err := h.db.Select(ctx, &links, `SELECT p.id AS id, COALESCE(p.name,'') AS name, `+sequence+` AS sequence FROM `+junction+` j JOIN `+table+` p ON p.id=j.`+col+` WHERE j.bookId = ? ORDER BY j.createdAt, j.rowid`, bookID)
	return links, err
}

// parent is the library's author or series of that name (case-insensitive,
// ASCII like the database), inserted from newRow when there is none.
func (h *Handler) parent(ctx context.Context, table, libraryID, name string, newRow func(id string) any) (id string, created bool, err error) {
	err = h.db.Get(ctx, &id, `SELECT id FROM `+table+` WHERE libraryId = ? AND name = ? COLLATE NOCASE LIMIT 1`, libraryID, name)
	if errors.Is(err, sql.ErrNoRows) {
		id = idgen.Must()
		err = h.db.Insert(ctx, table, newRow(id))
		created = err == nil
	}
	return id, created, err
}

func trimmed(names []string) (list []string, wanted map[string]bool) {
	wanted = map[string]bool{}
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			list = append(list, name)
			wanted[strings.ToLower(name)] = true
		}
	}
	return list, wanted
}

// seriesCreated is Series.toOldJSON of a freshly created series, which has no
// description attribute (server/models/Series.js:168-178): the series_added
// payload of a quick match.
type seriesCreated struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	NameIgnorePrefix string `json:"nameIgnorePrefix"`
	AddedAt          int64  `json:"addedAt"`
	UpdatedAt        int64  `json:"updatedAt"`
	LibraryID        string `json:"libraryId"`
}

// authorEvent is Author.toOldJSON of an author created now, which has no
// asin, description or imagePath attributes loaded (Author.js:177-199).
type authorEvent struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	LibraryID string `json:"libraryId"`
	AddedAt   int64  `json:"addedAt"`
	UpdatedAt int64  `json:"updatedAt"`
}

// authorExpandedEvent is Author.toOldJSONExpanded.
type authorExpandedEvent struct {
	authorEvent
	ASIN        *string `json:"asin"`
	Description *string `json:"description"`
	ImagePath   *string `json:"imagePath"`
	NumBooks    int     `json:"numBooks"`
}

// authorUpdated announces author_updated for an author that still has books
// (PATCH only).
func (h *Handler) authorUpdated(ctx context.Context, c *changes, id string, src source) error {
	if src != fromRequest {
		return nil
	}
	var n int
	if err := h.db.Get(ctx, &n, `SELECT COUNT(*) FROM bookAuthors WHERE authorId = ?`, id); err != nil || n == 0 {
		return err
	}
	var a db.Author
	if err := h.db.Get(ctx, &a, `SELECT * FROM authors WHERE id = ?`, id); err != nil {
		return err
	}
	c.events = append(c.events, event{"author_updated", authorExpandedEvent{
		authorEvent{a.ID, db.Val(a.Name), db.Val(a.LibraryID), a.CreatedAt.UnixMilli(), a.UpdatedAt.UnixMilli()}, a.ASIN, a.Description, a.ImagePath, n}})
	return nil
}

// linkAuthors makes the book's authors exactly names, creating missing ones:
// removal first, then additions (Book.updateAuthorsFromRequest, Book.js:434-466).
func (h *Handler) linkAuthors(ctx context.Context, c *changes, libraryID, bookID string, names []string, src source) (bool, error) {
	wants, wanted := trimmed(names)
	order, err := h.currentLinks(ctx, "authors", "bookAuthors", "authorId", "NULL", bookID)
	if err != nil {
		return false, err
	}
	have := map[string]bool{}
	changed := false
	for _, l := range order {
		key := strings.ToLower(l.Name)
		if wanted[key] {
			have[key] = true
			continue
		}
		if _, err := h.db.Exec(ctx, `DELETE FROM bookAuthors WHERE bookId = ? AND authorId = ?`, bookID, l.ID); err != nil {
			return false, err
		}
		changed = true
		c.authorsUnlinked = append(c.authorsUnlinked, l.ID)
		if err := h.authorUpdated(ctx, c, l.ID, src); err != nil {
			return false, err
		}
	}
	var clk clock
	for _, name := range wants {
		key := strings.ToLower(name)
		if have[key] {
			continue
		}
		have[key] = true
		now := clk.now()
		id, created, err := h.parent(ctx, "authors", libraryID, name, func(id string) any {
			return db.Author{ID: id, Name: &name, LastFirst: db.Ptr(text.ToLastFirst(name)), LibraryID: &libraryID, CreatedAt: now, UpdatedAt: now}
		})
		if err != nil {
			return false, err
		}
		if err := h.db.Insert(ctx, "bookAuthors", db.BookAuthor{ID: idgen.Must(), CreatedAt: now, BookID: &bookID, AuthorID: &id}); err != nil {
			return false, err
		}
		changed = true
		c.authorsAdded = append(c.authorsAdded, filterdata.Named{ID: id, Name: name})
		if created {
			c.events = append(c.events, event{"author_added", authorEvent{id, name, libraryID, now.UnixMilli(), now.UnixMilli()}})
		} else if err := h.authorUpdated(ctx, c, id, src); err != nil {
			return false, err
		}
	}
	return changed, nil
}

func samePtr(a, b *string) bool { return (a == nil) == (b == nil) && db.Val(a) == db.Val(b) }

// linkSeries makes the book's series exactly the given ones with their
// sequences (Book.updateSeriesFromRequest, Book.js:495-545).
func (h *Handler) linkSeries(ctx context.Context, c *changes, libraryID, bookID string, series []SeriesPatch, src source) (bool, error) {
	var wants []SeriesPatch
	wanted := map[string]bool{}
	for _, s := range series {
		if s.Name = strings.TrimSpace(s.Name); s.Name != "" {
			wants = append(wants, s)
			wanted[strings.ToLower(s.Name)] = true
		}
	}
	order, err := h.currentLinks(ctx, "series", "bookSeries", "seriesId", "j.sequence", bookID)
	if err != nil {
		return false, err
	}
	current := map[string]link{}
	changed := false
	for _, l := range order {
		key := strings.ToLower(l.Name)
		if wanted[key] {
			current[key] = l
			continue
		}
		if _, err := h.db.Exec(ctx, `DELETE FROM bookSeries WHERE bookId = ? AND seriesId = ?`, bookID, l.ID); err != nil {
			return false, err
		}
		changed = true
		c.seriesUnlinked = append(c.seriesUnlinked, l.ID)
	}
	var clk clock
	prefixes := h.prefixes()
	for _, w := range wants {
		key := strings.ToLower(w.Name)
		if l, ok := current[key]; ok {
			if w.keepSequence || samePtr(l.Sequence, w.Sequence) {
				continue
			}
			if _, err := h.db.Exec(ctx, `UPDATE bookSeries SET sequence = ? WHERE bookId = ? AND seriesId = ?`, w.Sequence, bookID, l.ID); err != nil {
				return false, err
			}
			changed = true
			l.Sequence = w.Sequence
			current[key] = l
			continue
		}
		now := clk.now()
		name := w.Name
		id, created, err := h.parent(ctx, "series", libraryID, name, func(id string) any {
			return db.Series{ID: id, Name: &name, NameIgnorePrefix: db.Ptr(text.TitleIgnorePrefix(name, prefixes)), LibraryID: &libraryID, CreatedAt: now, UpdatedAt: now}
		})
		if err != nil {
			return false, err
		}
		if err := h.db.Insert(ctx, "bookSeries", db.BookSeries{ID: idgen.Must(), Sequence: w.Sequence, CreatedAt: now, BookID: &bookID, SeriesID: &id}); err != nil {
			return false, err
		}
		current[key] = link{ID: id, Name: name, Sequence: w.Sequence}
		changed = true
		if created && src == fromMatch {
			ms := now.UnixMilli()
			c.events = append(c.events, event{"series_added", seriesCreated{ID: id, Name: name, NameIgnorePrefix: text.PrefixAtEnd(name, prefixes), AddedAt: ms, UpdatedAt: ms, LibraryID: libraryID}})
		}
	}
	return changed, nil
}
