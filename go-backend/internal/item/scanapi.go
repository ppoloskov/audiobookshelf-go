package item

import (
	"context"
	"slices"
)

// This file is the doorway the library scanner uses to link a scanned book to
// its authors and series, sweep the parents that lost their last book, and
// delete podcast episodes whose files disappeared. The scanner builds its own
// Handler with New (Deps.Filters only) and calls these methods.

// ScannedBook names what a scan found for one book.
type ScannedBook struct {
	LibraryID, BookID string
	// Authors replace the book's authors; empty leaves them as they are.
	Authors []string
	// Series and Sequence replace the book's series; an empty Series leaves
	// them as they are, an empty Sequence keeps a stored sequence.
	Series, Sequence string
}

// ScanLinks is what linking one scanned book changed, to announce once the
// transaction has committed.
type ScanLinks struct {
	c changes
	// AuthorsChanged are the authors that already existed and gained or lost a
	// book: their book counts need announcing.
	AuthorsChanged []string
	// AuthorsUnlinked and SeriesUnlinked may now be empty and are swept.
	AuthorsUnlinked, SeriesUnlinked []string
}

// LinkScanned links the book inside the caller's transaction. Names match
// case-insensitively, as every other author and series edit does.
func (h *Handler) LinkScanned(ctx context.Context, b ScannedBook) (ScanLinks, error) {
	var out ScanLinks
	if len(b.Authors) > 0 {
		if _, err := h.linkAuthors(ctx, &out.c, b.LibraryID, b.BookID, b.Authors, fromMatch); err != nil {
			return out, err
		}
	}
	if b.Series != "" {
		series := []SeriesPatch{{Name: b.Series, keepSequence: b.Sequence == ""}}
		if b.Sequence != "" {
			series[0].Sequence = &b.Sequence
		}
		if _, err := h.linkSeries(ctx, &out.c, b.LibraryID, b.BookID, series, fromMatch); err != nil {
			return out, err
		}
	}
	created := map[string]bool{}
	for _, e := range out.c.events {
		if added, ok := e.payload.(authorEvent); ok && e.name == "author_added" {
			created[added.ID] = true
		}
	}
	for _, a := range out.c.authorsAdded {
		if !created[a.ID] {
			out.AuthorsChanged = append(out.AuthorsChanged, a.ID)
		}
	}
	out.AuthorsUnlinked, out.SeriesUnlinked = out.c.authorsUnlinked, out.c.seriesUnlinked
	out.AuthorsChanged = append(out.AuthorsChanged, out.AuthorsUnlinked...)
	return out, nil
}

// Announce sends author_added. The scanner drops the filter menus once, when
// the scan ends.
func (l ScanLinks) Announce(h *Handler) {
	// A scan reports book counts once at the end and creates series silently.
	l.c.events = slices.DeleteFunc(l.c.events, func(e event) bool { return e.name != "author_added" })
	l.c.announce(h.emit)
}

// SweepOrphans removes authors and series that no book uses any more.
func (h *Handler) SweepOrphans(ctx context.Context, authorIDs, seriesIDs []string) {
	h.sweepAuthors(ctx, authorIDs)
	h.sweepSeries(ctx, seriesIDs)
}

// EpisodeRemoval is what deleting episodes left to announce.
type EpisodeRemoval struct{ playlists []PlaylistChange }

// DeleteEpisodes removes podcast episodes with their progress and playlist
// entries inside the caller's transaction.
func (h *Handler) DeleteEpisodes(ctx context.Context, episodeIDs []string) (EpisodeRemoval, error) {
	playlists, err := DeleteEpisodeRows(ctx, h.db, episodeIDs)
	return EpisodeRemoval{playlists}, err
}

// Announce sends playlist_updated and playlist_removed after the commit.
func (r EpisodeRemoval) Announce(ctx context.Context, h *Handler) {
	h.announcePlaylists(ctx, r.playlists)
}
