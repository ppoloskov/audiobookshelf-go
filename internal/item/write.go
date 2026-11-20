package item

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"monstrobook/internal/auth"
	"monstrobook/internal/cronspec"
	"monstrobook/internal/db"
	"monstrobook/internal/httpx"
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
		return h.updatePodcast(ctx, &out, p.podcast, src)
	})
	return out, err
}

func (h *Handler) updateBook(ctx context.Context, out *outcome, p *BookPatch, src source) error {
	row, err := getRow[db.Book](ctx, h.db, "books", out.ref.MediaID)
	if err != nil {
		return err
	}
	cols := p.apply(&row, h.prefixes(), src)
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

func (h *Handler) updatePodcast(ctx context.Context, out *outcome, p *PodcastPatch, src source) error {
	row, err := getRow[db.Podcast](ctx, h.db, "podcasts", out.ref.MediaID)
	if err != nil {
		return err
	}
	cols := p.apply(&row, h.prefixes(), src)
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
	return h.stampItem(ctx, itemID, now)
}

// finish is what every media write does once its transaction has committed,
// in one order: series that lost their last book are removed, the events of the
// new links go out and the filter menus are dropped, authors that lost their last book are
// removed (LibraryItemController.js:233-262), then each updated item is saved
// and announced and podcast cron entries are refreshed (js:273-280). A batch
// finishes once for all its items, so a series another item relinks is not swept.
func (h *Handler) finish(ctx context.Context, wantLast bool, outs ...outcome) (*Item, error) {
	var unlinked changes
	for _, o := range outs {
		unlinked.merge(changes{authorsUnlinked: o.changes.authorsUnlinked, seriesUnlinked: o.changes.seriesUnlinked})
	}
	h.sweepSeries(ctx, unlinked.seriesUnlinked)
	for _, o := range outs {
		o.changes.announce(h.emit)
		h.deps.Filters.Invalidate(o.ref.LibraryID)
	}
	h.sweepAuthors(ctx, unlinked.authorsUnlinked)
	var last *Item
	for i, o := range outs {
		if !o.updated {
			continue
		}
		if h.deps.WriteMetadata != nil {
			if err := h.deps.WriteMetadata(ctx, o.ref.ID); err != nil {
				return nil, err
			}
		}
		needItem := wantLast && i == len(outs)-1
		it, err := h.announceItem(ctx, o.ref.ID, needItem)
		if err != nil {
			return nil, err
		}
		if needItem {
			last = it
		}
		if o.reschedule && h.deps.ScheduleChanged != nil {
			h.deps.ScheduleChanged(ctx, o.ref.ID)
		}
	}
	if wantLast && last == nil && len(outs) > 0 {
		return Get(ctx, h.db, outs[len(outs)-1].ref.ID, Options{Episodes: true})
	}
	return last, nil
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
	_, err := h.announceItem(ctx, itemID, false)
	return err
}

// announceItem loads the item when listeners exist or needItem is set, sends
// item_updated with its expanded view, and returns the loaded item.
func (h *Handler) announceItem(ctx context.Context, itemID string, needItem bool) (*Item, error) {
	if !needItem && h.emit == nil {
		return nil, nil
	}
	it, err := Get(ctx, h.db, itemID, Options{Episodes: true})
	if err != nil {
		return nil, err
	}
	if h.emit != nil {
		h.emit.ToAll("item_updated", it.Expanded(h.prefixes()))
	}
	return it, nil
}

// announceHTTP announces item_updated and maps any reload failure to a 500.
func (h *Handler) announceHTTP(ctx context.Context, itemID string) error {
	if _, err := h.announceItem(ctx, itemID, false); err != nil {
		return httpx.Internal("database read failed", err)
	}
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
	ref, err := h.writable(ctx, user, r.PathValue("id"))
	if err != nil {
		return err
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
	it, err := h.finish(ctx, true, out)
	if err != nil {
		return httpx.Internal("metadata file write failed", err)
	}
	return httpx.JSON(w, r, updateResult{out.updated, it.Full()})
}
