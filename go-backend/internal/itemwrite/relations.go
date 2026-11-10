// Package itemwrite is the one write path for the book-to-author and
// book-to-series relations and for the cleanup that follows them. The PATCH
// media route, batch update, quick match and the scanner all link a book to
// authors and series, drop what a new list leaves out, and then sweep the
// authors and series that no book uses any more. They differ only in a few
// rules, which the callers pass as Rules.
package itemwrite

import (
	"audiobookshelf-compat/internal/ids"
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/nameparse"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/textsort"
	"audiobookshelf-compat/internal/wire"
)

type Rules struct {
	Exact         bool // parents match by exact name instead of case-insensitively (scanner)
	Replace       bool // links the list does not name are removed
	AuthorUpdates bool // author_updated for a reused author and for one that keeps books after an unlink (PATCH)
	SeriesAdded   bool // series_added for a created series (quick match)
}

// Series is one series of a book. A nil sequence preserves an existing value
// and defaults to SQL NULL for a new link; a nonnil sequence carries its value.
type Series struct {
	Name     string
	Sequence *sql.NullString
}

// Event is a socket event a relation edit announces.
type Event struct {
	Name    string
	Payload any
}

// Changes collects what relation edits did, so the caller can announce it once
// the transaction has committed.
type Changes struct {
	Events          []Event
	AuthorsAdded    []filterdata.Named // authors newly linked, for the filter data
	SeriesAdded     []filterdata.Named
	AuthorsRelinked []string // existing authors that gained a book
	AuthorsUnlinked []string
	SeriesUnlinked  []string
}

func (c *Changes) Merge(o Changes) {
	c.Events = append(c.Events, o.Events...)
	c.AuthorsAdded = append(c.AuthorsAdded, o.AuthorsAdded...)
	c.SeriesAdded = append(c.SeriesAdded, o.SeriesAdded...)
	c.AuthorsRelinked = append(c.AuthorsRelinked, o.AuthorsRelinked...)
	c.AuthorsUnlinked = append(c.AuthorsUnlinked, o.AuthorsUnlinked...)
	c.SeriesUnlinked = append(c.SeriesUnlinked, o.SeriesUnlinked...)
}

// Clock stamps rows inserted one after another. Sequelize stamps every create
// separately, so books keep their authors and series in insertion order when
// read back by createdAt; each stamp is a millisecond later than the one before.
type Clock struct{ last time.Time }

func (c *Clock) Now() string {
	t := time.Now().UTC().Truncate(time.Millisecond)
	if !t.After(c.last) {
		t = c.last.Add(time.Millisecond)
	}
	c.last = t
	return dbtime.Format(t)
}

type link struct {
	id, name string
	seq      sql.NullString
}

// key is how a name is compared with the book's current parents.
func (r Rules) key(name string) string {
	if r.Exact {
		return name
	}
	return strings.ToLower(name)
}

// currentLinks are the book's parents in link order and by key. seq is the
// column that holds the link's sequence ("NULL" for authors).
func currentLinks(ctx context.Context, tx *sql.Tx, table, junction, col, seq, bookID string, r Rules) ([]link, map[string]link, error) {
	rows, err := tx.QueryContext(ctx, `SELECT p.id,p.name,`+seq+` FROM `+junction+` j JOIN `+table+` p ON p.id=j.`+col+` WHERE j.bookId=? ORDER BY j.createdAt,j.rowid`, bookID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var order []link
	byKey := map[string]link{}
	for rows.Next() {
		var l link
		if err := rows.Scan(&l.id, &l.name, &l.seq); err != nil {
			return nil, nil, err
		}
		order = append(order, l)
		byKey[r.key(l.name)] = l
	}
	return order, byKey, rows.Err()
}

// parentID is the library's author or series of that name, inserted with its
// derived sort name (lastFirst or nameIgnorePrefix) when there is none.
func parentID(ctx context.Context, tx *sql.Tx, table, derivedCol, derived, libraryID, name, now string, r Rules) (id string, created bool, err error) {
	match := `name=?`
	if !r.Exact {
		match += ` COLLATE NOCASE`
	}
	err = tx.QueryRowContext(ctx, `SELECT id FROM `+table+` WHERE libraryId=? AND `+match+` LIMIT 1`, libraryID, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		id = ids.Must()
		_, err = tx.ExecContext(ctx, `INSERT INTO `+table+`(id,name,`+derivedCol+`,libraryId,createdAt,updatedAt) VALUES(?,?,?,?,?,?)`, id, name, derived, libraryID, now, now)
		created = err == nil
	}
	return id, created, err
}

// LinkAuthors links the book to the named authors, creating the missing ones.
func (c *Changes) LinkAuthors(ctx context.Context, tx *sql.Tx, libraryID, bookID string, names []string, r Rules) (bool, error) {
	var wants []string
	wanted := map[string]bool{}
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			wants = append(wants, name)
			wanted[r.key(name)] = true
		}
	}
	order, current, err := currentLinks(ctx, tx, "authors", "bookAuthors", "authorId", "NULL", bookID, r)
	if err != nil {
		return false, err
	}
	changed := false
	// Book.updateAuthorsFromRequest removes first, then adds (Book.js:434-466).
	if r.Replace {
		for _, l := range order {
			if wanted[r.key(l.name)] {
				continue
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM bookAuthors WHERE bookId=? AND authorId=?`, bookID, l.id); err != nil {
				return false, err
			}
			changed = true
			c.AuthorsUnlinked = append(c.AuthorsUnlinked, l.id)
			n, err := authorBookCount(ctx, tx, l.id)
			if err != nil {
				return false, err
			}
			if n > 0 && r.AuthorUpdates {
				payload, err := authorPayload(ctx, tx, l.id, false, n)
				if err != nil {
					return false, err
				}
				c.Events = append(c.Events, Event{"author_updated", payload})
			}
		}
	}
	var clock Clock
	done := map[string]bool{}
	for _, name := range wants {
		key := r.key(name)
		if done[key] {
			continue
		}
		done[key] = true
		if _, ok := current[key]; ok {
			continue
		}
		now := clock.Now()
		id, created, err := parentID(ctx, tx, "authors", "lastFirst", nameparse.ToLastFirst(name), libraryID, name, now, r)
		if err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO bookAuthors(id,bookId,authorId,createdAt) VALUES(?,?,?,?)`, ids.Must(), bookID, id, now); err != nil {
			return false, err
		}
		changed = true
		c.AuthorsAdded = append(c.AuthorsAdded, filterdata.Named{ID: id, Name: name})
		if created {
			payload, err := authorPayload(ctx, tx, id, true, 0)
			if err != nil {
				return false, err
			}
			c.Events = append(c.Events, Event{"author_added", payload})
			continue
		}
		c.AuthorsRelinked = append(c.AuthorsRelinked, id)
		if r.AuthorUpdates {
			n, err := authorBookCount(ctx, tx, id)
			if err != nil {
				return false, err
			}
			payload, err := authorPayload(ctx, tx, id, false, n)
			if err != nil {
				return false, err
			}
			c.Events = append(c.Events, Event{"author_updated", payload})
		}
	}
	return changed, nil
}

// LinkSeries links the book to the given series with their sequences.
func (c *Changes) LinkSeries(ctx context.Context, tx *sql.Tx, libraryID, bookID string, series []Series, prefixes []string, r Rules) (bool, error) {
	var wants []Series
	wanted := map[string]bool{}
	for _, s := range series {
		if s.Name = strings.TrimSpace(s.Name); s.Name != "" {
			wants = append(wants, s)
			wanted[r.key(s.Name)] = true
		}
	}
	order, current, err := currentLinks(ctx, tx, "series", "bookSeries", "seriesId", "j.sequence", bookID, r)
	if err != nil {
		return false, err
	}
	changed := false
	if r.Replace {
		for _, l := range order {
			if wanted[r.key(l.name)] {
				continue
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM bookSeries WHERE bookId=? AND seriesId=?`, bookID, l.id); err != nil {
				return false, err
			}
			changed = true
			c.SeriesUnlinked = append(c.SeriesUnlinked, l.id)
		}
	}
	var clock Clock
	for _, w := range wants {
		key := r.key(w.Name)
		var seq sql.NullString
		if w.Sequence != nil {
			seq = *w.Sequence
		}
		if l, ok := current[key]; ok {
			if w.Sequence == nil || l.seq == seq {
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE bookSeries SET sequence=? WHERE bookId=? AND seriesId=?`, store.NullString(seq), bookID, l.id); err != nil {
				return false, err
			}
			changed = true
			l.seq = seq
			current[key] = l
			continue
		}
		now := clock.Now()
		id, created, err := parentID(ctx, tx, "series", "nameIgnorePrefix", textsort.TitleIgnorePrefix(w.Name, prefixes), libraryID, w.Name, now, r)
		if err != nil {
			return false, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO bookSeries(id,bookId,seriesId,sequence,createdAt) VALUES(?,?,?,?,?)`, ids.Must(), bookID, id, store.NullString(seq), now); err != nil {
			return false, err
		}
		current[key] = link{id: id, name: w.Name, seq: seq}
		changed = true
		c.SeriesAdded = append(c.SeriesAdded, filterdata.Named{ID: id, Name: w.Name})
		if created && r.SeriesAdded {
			ms, err := dbtime.OptionalMillis(now)
			if err != nil || ms == nil {
				return false, errors.Join(err, errors.New("itemwrite: unreadable series timestamp"))
			}
			c.Events = append(c.Events, Event{"series_added", wire.SeriesCreated{ID: id, Name: w.Name, NameIgnorePrefix: textsort.PrefixAtEnd(w.Name, prefixes), AddedAt: *ms, UpdatedAt: *ms, LibraryID: libraryID}})
		}
	}
	return changed, nil
}

// authorPayload is Author.toOldJSON (+numBooks for toOldJSONExpanded). A
// freshly created author has no asin/description/imagePath attributes loaded,
// so those keys are absent from its payload (Author.js:177-199).
func authorPayload(ctx context.Context, tx *sql.Tx, id string, created bool, numBooks int) (map[string]any, error) {
	var name, libraryID, createdAt, updatedAt string
	var asin, description, imagePath sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT name,asin,description,imagePath,libraryId,createdAt,updatedAt FROM authors WHERE id=?`, id).Scan(&name, &asin, &description, &imagePath, &libraryID, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	msOf := func(value string) int64 {
		if t, ok := dbtime.Parse(value); ok {
			return t.UnixMilli()
		}
		return 0
	}
	payload := map[string]any{"id": id, "name": name, "libraryId": libraryID, "addedAt": msOf(createdAt), "updatedAt": msOf(updatedAt)}
	if !created {
		payload["asin"], payload["description"], payload["imagePath"] = store.NullString(asin), store.NullString(description), store.NullString(imagePath)
		payload["numBooks"] = numBooks
	}
	return payload, nil
}

func authorBookCount(ctx context.Context, tx *sql.Tx, authorID string) (int, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM bookAuthors WHERE authorId=?`, authorID).Scan(&n)
	return n, err
}
