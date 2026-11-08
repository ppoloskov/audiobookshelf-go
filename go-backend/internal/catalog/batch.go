// Batch routes mirroring server/controllers/LibraryItemController.js (pinned
// v2.37.2): POST /api/items/batch/update and /batch/delete.
package catalog

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/cronspec"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
	"context"
	"encoding/json"
	"net/http"
	"slices"
)

// itemsExpandedWhere is LibraryItem.findAllExpandedWhere({ id: ids })
// (LibraryItem.js:150-183): the existing items among ids in the order
// Sequelize returns them, by the first joined row ordered by bookAuthor then
// bookSeries creation time (see batchGet).
func (h *Handler) itemsExpandedWhere(ctx context.Context, ids []string) ([]itemScope, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := h.db.QueryContext(ctx, `SELECT `+itemScopeColumns+` FROM libraryItems li LEFT JOIN books b ON b.id=li.mediaId AND li.mediaType='book' LEFT JOIN bookAuthors ba ON ba.bookId=b.id LEFT JOIN bookSeries bs ON bs.bookId=b.id WHERE li.id IN (`+store.Placeholders(len(ids))+`) ORDER BY ba.createdAt ASC, bs.createdAt ASC`, stringArgs(ids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	items := make([]itemScope, 0, len(ids))
	for rows.Next() {
		item, err := scanItemScope(rows)
		if err != nil {
			return nil, err
		}
		if !seen[item.id] {
			seen[item.id] = true
			items = append(items, item)
		}
	}
	return items, rows.Err()
}

// userCanAccessItems is ensureUserCanAccessLibraryItemsForBatch
// (LibraryItemController.js:48-56): every item must pass
// User.checkCanAccessLibraryItem (library, explicit content and tags).
func (h *Handler) userCanAccessItems(ctx context.Context, user store.User, items []itemScope) (bool, error) {
	perms := access.For(ctx, user)
	for _, item := range items {
		if !perms.Library(item.libraryID) {
			return false, nil
		}
		table := map[string]string{"book": "books", "podcast": "podcasts"}[item.mediaType]
		if table == "" {
			return false, nil
		}
		permissionSQL, permissionArgs := perms.MediaClause("b")
		var visible int
		query := `SELECT COUNT(*) FROM libraryItems li JOIN ` + table + ` b ON b.id=li.mediaId WHERE li.id=?` + permissionSQL
		if err := h.db.QueryRowContext(ctx, query, append([]any{item.id}, permissionArgs...)...).Scan(&visible); err != nil {
			return false, err
		}
		if visible == 0 {
			return false, nil
		}
	}
	return true, nil
}

// batchDelete is LibraryItemController.batchDelete (LibraryItemController.js:
// 558-621): each item is removed like ApiRouter.handleDeleteLibraryItem (with
// its files when hard=1), then the series and authors it leaves empty.
func (h *Handler) batchDelete(w http.ResponseWriter, r *http.Request) error {
	user := store.RequestUser(r)
	if !access.For(r.Context(), user).Delete() {
		logging.Warnf(`[LibraryItemController] User "%s" attempted to delete without permission`, user.Username)
		return httputil.Status(http.StatusForbidden, "")
	}
	ctx := r.Context()
	hard := looselyOne(r.URL.Query().Get("hard"))
	ids, ok := bodyStringList(jsonBodyObject(r)["libraryItemIds"])
	if !ok {
		return httputil.Status(http.StatusBadRequest, "Invalid request body")
	}
	items, err := h.itemsExpandedWhere(ctx, ids)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if len(items) == 0 {
		return httputil.Status(http.StatusNotFound, "")
	}
	allowed, err := h.userCanAccessItems(ctx, user, items)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if !allowed {
		return httputil.Status(http.StatusForbidden, "")
	}
	libraryID := items[0].libraryID
	for _, item := range items {
		var linked itemLinks
		if err := h.removeLibraryItem(ctx, item, hard, &linked); err != nil {
			return err
		}
		// The series check runs before the authors check (js:610-615).
		h.env().SweepSeries(ctx, linked.series)
		h.env().SweepAuthors(ctx, linked.authors)
	}
	h.filters.ResetIssues(ctx, h.db, libraryID) // Database.resetLibraryIssuesFilterData
	return httputil.OK(w, r)
}

// batchUpdate is LibraryItemController.batchUpdate (LibraryItemController.js:
// 630-735): every payload {id, mediaPayload} updates its item like
// PATCH /api/items/:id/media, the emptied series and authors are removed at
// the end, and the answer counts the items that changed.
func (h *Handler) batchUpdate(w http.ResponseWriter, r *http.Request) error {
	user := store.RequestUser(r)
	if !access.For(r.Context(), user).Update() {
		logging.Warnf(`[LibraryItemController] User "%s" attempted to batch update without permission`, user.Username)
		return httputil.Status(http.StatusForbidden, "")
	}
	ctx := r.Context()
	updates, ok := jsonBodyArray(r)
	if !ok || len(updates) == 0 {
		logging.Errorf("[LibraryItemController] Batch update failed. Invalid payload")
		return httputil.Status(http.StatusBadRequest, "")
	}
	type update struct {
		id      string
		payload map[string]json.RawMessage
	}
	parsed := make([]update, 0, len(updates))
	seen := map[string]bool{}
	var ids []string
	for _, raw := range updates {
		var entry map[string]json.RawMessage
		_ = json.Unmarshal(raw, &entry)
		var idValue any
		_ = json.Unmarshal(entry["id"], &idValue)
		if !jsonvalue.Truthy(idValue) {
			continue
		}
		id := jsonvalue.Text(idValue)
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
		var mediaPayload map[string]json.RawMessage
		_ = json.Unmarshal(entry["mediaPayload"], &mediaPayload)
		parsed = append(parsed, update{id: id, payload: mediaPayload})
	}
	if len(ids) == 0 || len(ids) != len(updates) {
		logging.Errorf("[LibraryItemController] Batch update failed. Each update payload must have a unique library item id")
		return httputil.Status(http.StatusBadRequest, "")
	}
	items, err := h.itemsExpandedWhere(ctx, ids)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if len(items) != len(updates) {
		logging.Errorf("[LibraryItemController] Batch update failed. Not all library items found")
		return httputil.Status(http.StatusNotFound, "")
	}
	allowed, err := h.userCanAccessItems(ctx, user, items)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if !allowed {
		return httputil.Status(http.StatusForbidden, "")
	}
	byID := make(map[string]itemScope, len(items))
	for _, item := range items {
		byID[item.id] = item
	}
	itemsUpdated := 0
	var writes []itemWrite
	for _, entry := range parsed {
		item := byID[entry.id]
		payload := entry.payload
		if payload == nil {
			payload = map[string]json.RawMessage{}
		}
		if item.mediaType == "podcast" {
			var schedule string
			if raw, present := payload["autoDownloadSchedule"]; present && json.Unmarshal(raw, &schedule) == nil && schedule != "" && !cronspec.Valid(schedule) {
				logging.Warnf(`[LibraryItemController] Invalid auto download schedule cron expression "%s" for library item "%s" - skipping update`, schedule, item.id)
				continue
			}
		}
		var metadata map[string]json.RawMessage
		if raw := payload["metadata"]; len(raw) > 0 && string(raw) != "null" {
			_ = json.Unmarshal(raw, &metadata)
		}
		result, err := h.applyMedia(ctx, item, payload, metadata)
		if err != nil {
			return httputil.Internal("database write failed", err)
		}
		writes = append(writes, itemWrite{itemID: item.id, libraryID: item.libraryID, changes: result.changes, updated: result.updated})
		if result.updated {
			logging.Debugf("[LibraryItemController] Updated library item media %q", item.id)
			itemsUpdated++
		}
	}
	if err := h.finishItemWrites(ctx, writes...); err != nil {
		return httputil.Internal("metadata file write failed", err)
	}
	return httputil.JSON(w, r, batchUpdateResult{Success: true, Updates: itemsUpdated})
}

// batchGet is LibraryItemController.batchGet: the expanded items of the posted
// ids, in findAllExpandedWhere order; unknown ids are skipped and one item the
// user may not see refuses the whole request.
func (h *Handler) batchGet(w http.ResponseWriter, r *http.Request) error {
	user, ctx := store.RequestUser(r), r.Context()
	var request struct {
		IDs []string `json:"libraryItemIds"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil || len(request.IDs) == 0 {
		return httputil.Status(http.StatusForbidden, "Invalid payload")
	}
	ids := slices.DeleteFunc(slices.Compact(slices.Sorted(slices.Values(request.IDs))), func(id string) bool { return id == "" })
	if len(ids) == 0 {
		return httputil.Status(http.StatusForbidden, "Invalid payload")
	}
	items, err := h.itemsExpandedWhere(ctx, ids)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	allowed, err := h.userCanAccessItems(ctx, user, items)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if !allowed {
		return httputil.Status(http.StatusForbidden, "")
	}
	visible := make([]string, len(items))
	for i, item := range items {
		visible[i] = item.id
	}
	selected, err := h.expandedItems(ctx, visible)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	return httputil.JSON(w, r, map[string]any{"libraryItems": selected})
}

func stringArgs(values []string) []any {
	args := make([]any, len(values))
	for i, value := range values {
		args[i] = value
	}
	return args
}
