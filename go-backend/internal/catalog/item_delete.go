package catalog

import (
	"audiobookshelf-compat/internal/itemwrite"
)

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
