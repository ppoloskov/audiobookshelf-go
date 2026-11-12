package libraries

import (
	"context"

	"audiobookshelf-compat/internal/db"
)

// FolderView is Folder.toOldJSON.
type FolderView struct {
	ID        string  `json:"id"`
	FullPath  string  `json:"fullPath"`
	LibraryID string  `json:"libraryId"`
	AddedAt   db.Time `json:"addedAt"`
}

// View is Library.toOldJSON, the one shape of a library in lists, details and
// the order and update responses and events.
type View struct {
	ID              string             `json:"id"`
	Name            string             `json:"name"`
	Folders         []FolderView       `json:"folders"`
	DisplayOrder    int64              `json:"displayOrder"`
	Icon            *string            `json:"icon"`
	MediaType       *string            `json:"mediaType"`
	Provider        *string            `json:"provider"`
	Settings        db.LibrarySettings `json:"settings"`
	LastScan        db.Time            `json:"lastScan"`
	LastScanVersion *string            `json:"lastScanVersion"`
	CreatedAt       db.Time            `json:"createdAt"`
	LastUpdate      db.Time            `json:"lastUpdate"`
	Stats           any                `json:"stats,omitempty"`
}

// created is the answer to a create: upstream serialises a freshly built
// instance there, so lastScanVersion, which was never persisted, is left out
// (clients ignore it, the other routes send null).
type created struct {
	View
	LastScanVersion *string `json:"lastScanVersion,omitempty"`
}

// views renders libraries with their folders (oldest first).
func (h *Handler) views(ctx context.Context, libs []db.Library) ([]View, error) {
	ids := make([]string, len(libs))
	for i, lib := range libs {
		ids[i] = lib.ID
	}
	folders, err := selectAll[db.LibraryFolder](ctx, h.db, `SELECT * FROM libraryFolders WHERE libraryId IN (?) ORDER BY createdAt, rowid`, ids)
	if err != nil {
		return nil, err
	}
	out := make([]View, len(libs))
	for i, lib := range libs {
		out[i] = View{ID: lib.ID, Name: db.Val(lib.Name), Folders: []FolderView{}, DisplayOrder: db.Val(lib.DisplayOrder),
			Icon: lib.Icon, MediaType: lib.MediaType, Provider: lib.Provider, Settings: lib.Settings.V, LastScan: lib.LastScan,
			LastScanVersion: lib.LastScanVersion, CreatedAt: lib.CreatedAt, LastUpdate: lib.UpdatedAt}
		if lib.LastScan.Millis() == 0 { // lastScan?.valueOf() || null
			out[i].LastScan = db.Time{}
		}
		for _, f := range folders {
			if db.Val(f.LibraryID) == lib.ID {
				out[i].Folders = append(out[i].Folders, FolderView{ID: f.ID, FullPath: db.Val(f.Path), LibraryID: lib.ID, AddedAt: f.CreatedAt})
			}
		}
	}
	return out, nil
}

// view renders one library; sql.ErrNoRows when it does not exist.
func (h *Handler) view(ctx context.Context, id string) (View, error) {
	var lib db.Library
	if err := h.db.Get(ctx, &lib, `SELECT * FROM libraries WHERE id = ?`, id); err != nil {
		return View{}, err
	}
	views, err := h.views(ctx, []db.Library{lib})
	if err != nil {
		return View{}, err
	}
	return views[0], nil
}
