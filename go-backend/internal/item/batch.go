package item

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
)

// batchIDs is the body of batch get, delete and quick match.
type batchIDs struct {
	IDs []string `json:"libraryItemIds"`
}

// batchGet is LibraryItemController.batchGet: the expanded items of the posted
// ids in findAllExpandedWhere order; unknown ids are skipped and one item the
// user may not see refuses the whole request. An unusable body is a 403, as
// upstream answers.
func (h *Handler) batchGet(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	ctx := r.Context()
	body, err := httpx.Decode[batchIDs](w, r)
	if err != nil {
		return httpx.Error(http.StatusForbidden, "Invalid payload")
	}
	ids := slices.DeleteFunc(slices.Compact(slices.Sorted(slices.Values(body.IDs))), func(id string) bool { return id == "" })
	if len(ids) == 0 {
		return httpx.Error(http.StatusForbidden, "Invalid payload")
	}
	refs, err := h.refsInQueryOrder(ctx, ids)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	if allowed, err := h.canAccessAll(ctx, user, refs); err != nil {
		return httpx.Internal("database read failed", err)
	} else if !allowed {
		return httpx.Forbidden()
	}
	order := make([]string, len(refs))
	for i, ref := range refs {
		order[i] = ref.ID
	}
	loaded, err := Load(ctx, h.db, order, Options{Episodes: true})
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	prefixes := h.prefixes()
	items := make([]Expanded, len(loaded))
	for i, it := range loaded {
		items[i] = it.Expanded(prefixes)
	}
	return httpx.JSON(w, r, struct {
		LibraryItems []Expanded `json:"libraryItems"`
	}{items})
}

// batchTargets reads the ids of a batch body (400 when there are none) and
// returns the existing items among them.
func (h *Handler) batchTargets(w http.ResponseWriter, r *http.Request) ([]itemRef, error) {
	body, err := httpx.Decode[batchIDs](w, r)
	if err != nil || len(body.IDs) == 0 {
		return nil, httpx.BadRequest("Invalid request body")
	}
	refs, err := h.refsInQueryOrder(r.Context(), body.IDs)
	if err != nil {
		return nil, httpx.Internal("database read failed", err)
	}
	return refs, nil
}

// batchDelete is LibraryItemController.batchDelete (js:558-621): each item is
// removed like DELETE /api/items/{id}, the series and then the authors it
// leaves empty are swept after each.
func (h *Handler) batchDelete(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	ctx := r.Context()
	if !user.CanDelete() {
		slog.Warn("user attempted to batch delete without permission", "user", db.Val(user.Username))
		return httpx.Forbidden()
	}
	refs, err := h.batchTargets(w, r)
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		return httpx.NotFound()
	}
	if allowed, err := h.canAccessAll(ctx, user, refs); err != nil {
		return httpx.Internal("database read failed", err)
	} else if !allowed {
		return httpx.Forbidden()
	}
	var files func(context.Context, itemRef)
	if r.URL.Query().Get("hard") == "1" {
		files = h.removeFiles
	}
	for _, ref := range refs {
		rem, err := h.deleteItem(ctx, ref, files)
		if err != nil {
			return httpx.Internal("database write failed", err)
		}
		h.sweepSeries(ctx, rem.series) // series before authors (js:610-615)
		h.sweepAuthors(ctx, rem.authors)
	}
	h.deps.Filters.Invalidate(refs[0].LibraryID)
	return httpx.Status(w, http.StatusOK)
}

// batchUpdateEntry is one {id, mediaPayload} of a batch update.
type batchUpdateEntry struct {
	ID           string          `json:"id"`
	MediaPayload json.RawMessage `json:"mediaPayload"`
}

// batchUpdate is LibraryItemController.batchUpdate (js:630-735): every entry
// updates its item like PATCH media, emptied series and authors are swept once
// at the end, and the answer counts the items that changed.
func (h *Handler) batchUpdate(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	ctx := r.Context()
	if !user.CanUpdate() {
		slog.Warn("user attempted to batch update without permission", "user", db.Val(user.Username))
		return httpx.Forbidden()
	}
	entries, err := httpx.DecodeLimit[[]batchUpdateEntry](w, r, 10<<20)
	if err != nil || len(entries) == 0 {
		return httpx.BadRequest("")
	}
	ids := make([]string, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
	}
	if slices.Contains(ids, "") || len(slices.Compact(slices.Sorted(slices.Values(ids)))) != len(ids) {
		slog.Error("batch update failed: each update must have a unique library item id")
		return httpx.BadRequest("")
	}
	refs, err := h.refsInQueryOrder(ctx, ids)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	if len(refs) != len(entries) {
		slog.Error("batch update failed: not all library items found")
		return httpx.NotFound()
	}
	if allowed, err := h.canAccessAll(ctx, user, refs); err != nil {
		return httpx.Internal("database read failed", err)
	} else if !allowed {
		return httpx.Forbidden()
	}
	byID := make(map[string]itemRef, len(refs))
	for _, ref := range refs {
		byID[ref.ID] = ref
	}
	patches := make([]patch, len(entries))
	for i, e := range entries {
		payload := e.MediaPayload
		if len(payload) == 0 {
			payload = []byte("{}")
		}
		if patches[i], err = parsePatch(byID[e.ID].MediaType, payload); err != nil {
			return httpx.BadRequest("Invalid request body")
		}
	}
	var outs []outcome
	count := 0
	for i, e := range entries {
		ref := byID[e.ID]
		if !patches[i].validSchedule() {
			slog.Warn("invalid auto download schedule cron expression, skipping update", "item", ref.ID)
			continue
		}
		out, err := h.update(ctx, ref, patches[i], fromRequest)
		if err != nil {
			return httpx.Internal("database write failed", err)
		}
		out.reschedule = false // batch update never reschedules (js:630-735)
		outs = append(outs, out)
		if out.updated {
			count++
		}
	}
	if err := h.finish(ctx, outs...); err != nil {
		return httpx.Internal("metadata file write failed", err)
	}
	return httpx.JSON(w, r, struct {
		Success bool `json:"success"`
		Updates int  `json:"updates"`
	}{true, count})
}
