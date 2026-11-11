package item

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/cronspec"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
)

// patch is a media payload of one of the two media types.
type patch struct {
	book    *BookPatch
	podcast *PodcastPatch
}

// parsePatch decodes a media payload for an item of mediaType. A payload that
// is not a JSON object, or has a field of the wrong type, is an error.
func parsePatch(mediaType string, raw []byte) (patch, error) {
	switch mediaType {
	case "book":
		var p BookPatch
		err := json.Unmarshal(raw, &p)
		return patch{book: &p}, err
	case "podcast":
		var p PodcastPatch
		err := json.Unmarshal(raw, &p)
		return patch{podcast: &p}, err
	}
	return patch{}, errors.New("unsupported media type " + mediaType)
}

// validSchedule reports whether the payload's cron expression, if any, is valid
// (LibraryItemController.js:225).
func (p patch) validSchedule() bool {
	s := ""
	if p.podcast != nil && p.podcast.AutoDownloadSchedule != nil {
		s = *p.podcast.AutoDownloadSchedule
	}
	return s == "" || cronspec.Valid(s)
}

// outcome is what applying a patch to one item changed.
type outcome struct {
	ref        itemRef
	changes    changes
	updated    bool
	reschedule bool // a podcast's auto-download flag or schedule changed
}

// update applies p to the item in one transaction: the media row's changed
// columns and, for books, the author and series links. It is the one write
// path of PATCH media, batch update and quick match. The caller runs finish.
func (h *Handler) update(ctx context.Context, ref itemRef, p patch, src source) (outcome, error) {
	out := outcome{ref: ref}
	err := h.db.Tx(ctx, func(ctx context.Context) error {
		if p.book != nil {
			return h.updateBook(ctx, &out, p.book, src)
		}
		return h.updatePodcast(ctx, &out, p.podcast)
	})
	return out, err
}

func (h *Handler) updateBook(ctx context.Context, out *outcome, p *BookPatch, src source) error {
	var row db.Book
	if err := h.db.Get(ctx, &row, `SELECT * FROM books WHERE id = ?`, out.ref.MediaID); err != nil {
		return err
	}
	cols := p.apply(&row, h.prefixes())
	out.updated = len(cols) > 0
	if m := p.Metadata; m != nil {
		if m.Authors != nil {
			names := make([]string, len(*m.Authors))
			for i, a := range *m.Authors {
				names[i] = a.Name
			}
			changed, err := h.linkAuthors(ctx, &out.changes, out.ref.LibraryID, row.ID, names, src)
			if err != nil {
				return err
			}
			out.updated = out.updated || changed
		}
		// A request with a nameless series entry leaves the series alone (Book.js:496).
		if m.Series != nil && (src == fromMatch || !slices.ContainsFunc(*m.Series, func(s SeriesPatch) bool { return strings.TrimSpace(s.Name) == "" })) {
			changed, err := h.linkSeries(ctx, &out.changes, out.ref.LibraryID, row.ID, *m.Series, src)
			if err != nil {
				return err
			}
			out.updated = out.updated || changed
		}
	}
	if !out.updated {
		return nil
	}
	row.UpdatedAt = db.Now()
	return h.save(ctx, "books", &row, append(cols, "updatedAt"), out.ref.ID, row.UpdatedAt)
}

func (h *Handler) updatePodcast(ctx context.Context, out *outcome, p *PodcastPatch) error {
	var row db.Podcast
	if err := h.db.Get(ctx, &row, `SELECT * FROM podcasts WHERE id = ?`, out.ref.MediaID); err != nil {
		return err
	}
	cols := p.apply(&row, h.prefixes())
	out.updated = len(cols) > 0
	out.reschedule = slices.Contains(cols, "autoDownloadEpisodes") || slices.Contains(cols, "autoDownloadSchedule")
	if !out.updated {
		return nil
	}
	row.UpdatedAt = db.Now()
	return h.save(ctx, "podcasts", &row, append(cols, "updatedAt"), out.ref.ID, row.UpdatedAt)
}

// save writes the changed columns of the media row and stamps the item.
func (h *Handler) save(ctx context.Context, table string, row any, cols columns, itemID string, now db.Time) error {
	if err := h.db.Update(ctx, table, row, cols...); err != nil {
		return err
	}
	return h.db.Update(ctx, "libraryItems", &db.LibraryItem{ID: itemID, UpdatedAt: now}, "updatedAt")
}

// finish is what every media write does once its transaction has committed,
// in one order: series that lost their last book are removed, the events and
// filter data of the new links go out, authors that lost their last book are
// removed (LibraryItemController.js:233-262), then each updated item is saved
// and announced and podcast cron entries are refreshed (js:273-280). A batch
// finishes once for all its items, so a series another item relinks is not swept.
func (h *Handler) finish(ctx context.Context, outs ...outcome) error {
	var unlinked changes
	for _, o := range outs {
		unlinked.merge(changes{authorsUnlinked: o.changes.authorsUnlinked, seriesUnlinked: o.changes.seriesUnlinked})
	}
	h.sweepSeries(ctx, unlinked.seriesUnlinked)
	for _, o := range outs {
		o.changes.announce(o.ref.LibraryID, h.deps.Filters, h.emit)
	}
	h.sweepAuthors(ctx, unlinked.authorsUnlinked)
	for _, o := range outs {
		if !o.updated {
			continue
		}
		if err := h.ItemChanged(ctx, o.ref.ID); err != nil {
			return err
		}
		if o.reschedule && h.deps.ScheduleChanged != nil {
			h.deps.ScheduleChanged(ctx, o.ref.ID)
		}
	}
	return nil
}

// ItemChanged ends an item mutation: the metadata file is rewritten and
// item_updated carries the expanded item. Other routes that edit an item
// (admin tag and genre changes) use it too.
func (h *Handler) ItemChanged(ctx context.Context, itemID string) error {
	if h.deps.WriteMetadata != nil {
		if err := h.deps.WriteMetadata(ctx, itemID); err != nil {
			return err
		}
	}
	if h.emit == nil { // nobody listens: skip building the expanded item
		return nil
	}
	it, err := Get(ctx, h.db, itemID, Options{Episodes: true})
	if err != nil {
		return err
	}
	h.emit.ToAll("item_updated", it.Expanded(h.prefixes()))
	return nil
}

// updateResult is the body of PATCH /api/items/{id}/media.
type updateResult struct {
	Updated     bool `json:"updated"`
	LibraryItem Full `json:"libraryItem"`
}

// updateMedia is LibraryItemController.updateMedia (js:208-291). Order: 404,
// 403 (library, then update permission), then the body.
func (h *Handler) updateMedia(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	ctx := r.Context()
	ref, err := h.loadRef(ctx, r.PathValue("id"))
	if err != nil {
		return err
	}
	if !user.CanAccessLibrary(ref.LibraryID) || !user.CanUpdate() {
		return httpx.Forbidden()
	}
	raw, err := httpx.DecodeLimit[json.RawMessage](w, r, 2<<20)
	if err != nil {
		return err
	}
	p, err := parsePatch(ref.MediaType, raw)
	if p.book == nil && p.podcast == nil {
		return httpx.BadRequest("")
	}
	if err != nil {
		return httpx.BadRequest("Invalid request body")
	}
	if !p.validSchedule() {
		return httpx.BadRequest("Invalid auto download schedule cron expression")
	}
	out, err := h.update(ctx, ref, p, fromRequest)
	if err != nil {
		return httpx.Internal("database write failed", err)
	}
	if err := h.finish(ctx, out); err != nil {
		return httpx.Internal("metadata file write failed", err)
	}
	it, err := Get(ctx, h.db, ref.ID, Options{Episodes: true})
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	return httpx.JSON(w, r, updateResult{out.updated, it.Full()})
}
