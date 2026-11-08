// Package itemwrite is the one write path for the book-to-author and
// book-to-series relations and for the cleanup that follows them. The PATCH
// media route, batch update, quick match and the scanner all link a book to
// authors and series, drop what a new list leaves out, and then sweep the
// authors and series that no book uses any more. They differ only in a few
// rules, which the callers pass as Rules.
package itemwrite

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

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
	KeepSequence  bool // a blank sequence leaves a stored one alone (scanner)
}

// Series is one series of a book with its sequence ("" for none).
type Series struct{ Name, Sequence string }

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

type kind struct {
	parent, junction, col string
	author                bool
	derived               func(name string) string // lastFirst or nameIgnorePrefix
	prefixes              []string                 // sorting prefixes of a series name
}

type want struct {
	name string
	seq  sql.NullString
}

type link struct {
	id, name string
	seq      sql.NullString
}

// LinkAuthors links the book to the named authors, creating the missing ones.
func (c *Changes) LinkAuthors(ctx context.Context, tx *sql.Tx, libraryID, bookID string, names []string, r Rules) (bool, error) {
	k := kind{parent: "authors", junction: "bookAuthors", col: "authorId", author: true, derived: nameparse.ToLastFirst}
	var wants []want
	for _, name := range names {
		if name = strings.TrimSpace(name); name != "" {
			wants = append(wants, want{name: name})
		}
	}
	return c.relate(ctx, tx, k, libraryID, bookID, wants, r)
}

// LinkSeries links the book to the given series with their sequences.
func (c *Changes) LinkSeries(ctx context.Context, tx *sql.Tx, libraryID, bookID string, series []Series, prefixes []string, r Rules) (bool, error) {
	k := kind{parent: "series", junction: "bookSeries", col: "seriesId", prefixes: prefixes, derived: func(name string) string { return textsort.TitleIgnorePrefix(name, prefixes) }}
	var wants []want
	for _, s := range series {
		if name := strings.TrimSpace(s.Name); name != "" {
			wants = append(wants, want{name: name, seq: sql.NullString{String: s.Sequence, Valid: s.Sequence != ""}})
		}
	}
	return c.relate(ctx, tx, k, libraryID, bookID, wants, r)
}

func (c *Changes) relate(ctx context.Context, tx *sql.Tx, k kind, libraryID, bookID string, wants []want, r Rules) (bool, error) {
	key := func(name string) string {
		if r.Exact {
			return name
		}
		return strings.ToLower(name)
	}
	seqSel := "NULL"
	if !k.author {
		seqSel = "j.sequence"
	}
	rows, err := tx.QueryContext(ctx, `SELECT p.id,p.name,`+seqSel+` FROM `+k.junction+` j JOIN `+k.parent+` p ON p.id=j.`+k.col+` WHERE j.bookId=? ORDER BY j.createdAt,j.rowid`, bookID)
	if err != nil {
		return false, err
	}
	var order []link
	current := map[string]link{}
	for rows.Next() {
		var l link
		if err := rows.Scan(&l.id, &l.name, &l.seq); err != nil {
			rows.Close()
			return false, err
		}
		order = append(order, l)
		current[key(l.name)] = l
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	changed := false
	wanted := map[string]bool{}
	for _, w := range wants {
		wanted[key(w.name)] = true
	}
	// Book.updateAuthorsFromRequest removes first, then adds (Book.js:434-466).
	if r.Replace {
		for _, l := range order {
			if wanted[key(l.name)] {
				continue
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+k.junction+` WHERE bookId=? AND `+k.col+`=?`, bookID, l.id); err != nil {
				return false, err
			}
			changed = true
			if !k.author {
				c.SeriesUnlinked = append(c.SeriesUnlinked, l.id)
				continue
			}
			c.AuthorsUnlinked = append(c.AuthorsUnlinked, l.id)
			if n := authorBookCount(ctx, tx, l.id); n > 0 && r.AuthorUpdates {
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
	for _, w := range wants {
		name := key(w.name)
		if done[name] {
			continue
		}
		done[name] = true
		if l, ok := current[name]; ok {
			if (!w.seq.Valid && r.KeepSequence) || l.seq == w.seq || k.author {
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE bookSeries SET sequence=? WHERE bookId=? AND seriesId=?`, store.NullString(w.seq), bookID, l.id); err != nil {
				return false, err
			}
			changed = true
			continue
		}
		now := clock.Now()
		match := `name=?`
		if !r.Exact {
			match += ` COLLATE NOCASE`
		}
		var id string
		created := false
		err := tx.QueryRowContext(ctx, `SELECT id FROM `+k.parent+` WHERE libraryId=? AND `+match+` LIMIT 1`, libraryID, w.name).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			created = true
			id = uuid.NewString()
			derivedCol := "nameIgnorePrefix"
			if k.author {
				derivedCol = "lastFirst"
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO `+k.parent+`(id,name,`+derivedCol+`,libraryId,createdAt,updatedAt) VALUES(?,?,?,?,?,?)`, id, w.name, k.derived(w.name), libraryID, now, now)
		}
		if err != nil {
			return false, err
		}
		if k.author {
			_, err = tx.ExecContext(ctx, `INSERT INTO bookAuthors(id,bookId,authorId,createdAt) VALUES(?,?,?,?)`, uuid.NewString(), bookID, id, now)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO bookSeries(id,bookId,seriesId,sequence,createdAt) VALUES(?,?,?,?,?)`, uuid.NewString(), bookID, id, store.NullString(w.seq), now)
		}
		if err != nil {
			return false, err
		}
		changed = true
		added := filterdata.Named{ID: id, Name: w.name}
		switch {
		case !k.author:
			c.SeriesAdded = append(c.SeriesAdded, added)
			if created && r.SeriesAdded {
				ms, _ := dbtime.OptionalMillis(now)
				c.Events = append(c.Events, Event{"series_added", wire.SeriesCreated{ID: id, Name: w.name, NameIgnorePrefix: textsort.PrefixAtEnd(w.name, k.prefixes), AddedAt: *ms, UpdatedAt: *ms, LibraryID: libraryID}})
			}
		case created:
			payload, err := authorPayload(ctx, tx, id, true, 0)
			if err != nil {
				return false, err
			}
			c.AuthorsAdded = append(c.AuthorsAdded, added)
			c.Events = append(c.Events, Event{"author_added", payload})
		default:
			c.AuthorsAdded = append(c.AuthorsAdded, added)
			c.AuthorsRelinked = append(c.AuthorsRelinked, id)
			if r.AuthorUpdates {
				payload, err := authorPayload(ctx, tx, id, false, authorBookCount(ctx, tx, id))
				if err != nil {
					return false, err
				}
				c.Events = append(c.Events, Event{"author_updated", payload})
			}
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

func authorBookCount(ctx context.Context, tx *sql.Tx, authorID string) int {
	var n int
	_ = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM bookAuthors WHERE authorId=?`, authorID).Scan(&n)
	return n
}
