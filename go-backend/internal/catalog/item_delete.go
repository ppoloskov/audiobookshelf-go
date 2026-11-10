package catalog

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/itemwrite"
	"audiobookshelf-compat/internal/store"
	"net/http"
)

func (h *Handler) deleteItem(w http.ResponseWriter, r *http.Request) error {
	user, ctx := store.RequestUser(r), r.Context()
	item, err := h.loadItem(ctx, r.PathValue("id"))
	if err != nil {
		return err
	}
	if perms := access.For(ctx, user); !perms.Library(item.libraryID) || !perms.Delete() {
		return httputil.Status(http.StatusForbidden, "")
	}
	if _, err := item.mediaTable(); err != nil {
		return err
	}
	target := deletionTarget(item)
	var changes itemwrite.DeleteChanges
	if r.URL.Query().Get("hard") == "1" {
		changes, err = h.deleter().DeleteHard(ctx, target)
	} else {
		changes, err = h.deleter().DeleteSoft(ctx, target)
	}
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	h.env().Sweep(ctx, changes.Authors, changes.Series)
	h.filters.ResetIssues(ctx, h.db, item.libraryID)
	w.WriteHeader(http.StatusOK)
	return nil
}

func deletionTarget(item itemScope) itemwrite.DeleteTarget {
	return itemwrite.DeleteTarget{
		ItemID: item.id, LibraryID: item.libraryID, MediaType: item.mediaType, MediaID: item.mediaID, Path: item.path,
	}
}

func (h *Handler) deleter() itemwrite.Deleter {
	itemMetadataDir := ""
	if h.cfg.MetadataPath != "" {
		itemMetadataDir = h.cfg.Dirs().Items
	}
	return itemwrite.Deleter{Env: itemwrite.Env{DB: h.db, Filters: h.filters, Emit: h.emit}, CoverCacheDir: h.cfg.Dirs().CoverCache, ItemMetadataDir: itemMetadataDir}
}
