package catalog

import (
	"context"

	"audiobookshelf-compat/internal/library"
)

// libraryFolderView and libraryView keep the catalog write responses on the
// library-owned shape while writes remain in catalog.
type libraryFolderView = library.FolderView
type libraryView = library.View

func (h *Handler) libraryView(ctx context.Context, id string) (libraryView, error) {
	return library.ReadView(ctx, h.db, id)
}
