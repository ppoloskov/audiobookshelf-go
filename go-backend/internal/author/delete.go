package author

import (
	"log/slog"
	"net/http"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
)

// delete is AuthorController.delete (js:233-258): the author row goes, its
// books lose the link and their metadata.json is rewritten.
func (h *Handler) delete(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	ctx := r.Context()
	a, err := h.load(r, user)
	if err != nil {
		return err
	}
	slog.Info("removing author", "name", db.Val(a.Name))
	if db.Val(a.ImagePath) != "" {
		h.purgeImageCache(a.ID)
	}
	itemIDs, err := h.bookItems(ctx, a.ID)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	if _, err := h.db.Exec(ctx, `DELETE FROM authors WHERE id = ?`, a.ID); err != nil {
		return httpx.Internal("database write failed", err)
	}
	if err := h.writeMetadata(ctx, itemIDs); err != nil {
		return httpx.Internal("metadata file write failed", err)
	}
	h.emit.ToAll("author_removed", viewOf(a, nil))
	h.deps.Filters.Invalidate(db.Val(a.LibraryID))
	return httpx.Status(w, http.StatusOK)
}
