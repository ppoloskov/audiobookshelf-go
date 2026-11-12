package library

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/ids"
)

// bodyLimit is the largest create or update body.
const bodyLimit = 2 << 20

// folderInput is a folder of a create or update body: an existing folder is
// named by id, a new one by fullPath (or path).
type folderInput struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	FullPath string `json:"fullPath"`
}

// makeFolder resolves a new folder's path to an absolute slash path and
// creates the directory.
func makeFolder(input folderInput) (string, error) {
	path := cmp.Or(input.FullPath, input.Path)
	if path == "" {
		return "", httpx.BadRequest("Invalid request folders")
	}
	abs, err := filepath.Abs(path)
	if err == nil {
		abs = filepath.ToSlash(abs)
		err = os.MkdirAll(abs, 0o755)
	}
	if err != nil {
		return "", httpx.BadRequest("Invalid folder directory")
	}
	return abs, nil
}

type createRequest struct {
	Name      string         `json:"name"`
	MediaType *string        `json:"mediaType"`
	Provider  *string        `json:"provider"`
	Icon      *string        `json:"icon"`
	Folders   *[]folderInput `json:"folders"`
	Settings  *settingsPatch `json:"settings"`
}

// create is POST /api/libraries (LibraryController.create).
func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	req, err := httpx.DecodeLimit[createRequest](w, r, bodyLimit)
	if err != nil {
		return err
	}
	if req.Name == "" {
		return httpx.BadRequest("Invalid request. Name must be a string")
	}
	text := func(value *string, fallback string) string { return cmp.Or(db.Val(value), fallback) }
	mediaType, provider, icon := text(req.MediaType, "book"), text(req.Provider, "google"), text(req.Icon, "database")
	if (req.MediaType != nil && *req.MediaType == "") || (req.Provider != nil && *req.Provider == "") || (req.Icon != nil && *req.Icon == "") {
		return httpx.BadRequest("Invalid request. mediaType, icon and provider must be strings")
	}
	if req.Folders == nil {
		return httpx.BadRequest("Invalid request. Folders must be an array of objects with path string")
	}
	if strings.HasPrefix(provider, "custom-") {
		var n int
		if err := h.db.Get(ctx, &n, `SELECT COUNT(*) FROM customMetadataProviders WHERE slug = ?`, provider); err != nil || n == 0 {
			return httpx.BadRequest("Invalid request. Custom metadata provider does not exist")
		}
	}
	paths := make([]string, len(*req.Folders))
	for i, folder := range *req.Folders {
		if paths[i], err = makeFolder(folder); err != nil {
			return err
		}
	}
	settings := defaultSettings(mediaType)
	if req.Settings != nil {
		req.Settings.apply(&settings, mediaType == "podcast")
	}
	id, now := ids.Must(), db.Now()
	err = h.db.Tx(ctx, func(ctx context.Context) error {
		var order int64
		if err := h.db.Get(ctx, &order, `SELECT COALESCE(MAX(displayOrder),0)+1 FROM libraries`); err != nil {
			return err
		}
		lib := db.Library{ID: id, Name: &req.Name, DisplayOrder: &order, Icon: &icon, MediaType: &mediaType, Provider: &provider,
			Settings: db.JSON[db.LibrarySettings]{V: settings}, ExtraData: db.JSON[*db.LibraryExtra]{V: &db.LibraryExtra{}}, CreatedAt: now, UpdatedAt: now}
		if err := h.db.Insert(ctx, "libraries", &lib); err != nil {
			return err
		}
		for _, path := range paths {
			if err := h.insertFolder(ctx, id, path, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return httpx.Internal("database write failed", err)
	}
	view, err := h.view(ctx, id)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	h.changed(id)
	h.emit.ToAll("library_added", created{View: view})
	return httpx.JSON(w, r, created{View: view})
}

func (h *Handler) insertFolder(ctx context.Context, libraryID, path string, now db.Time) error {
	return h.db.Insert(ctx, "libraryFolders", &db.LibraryFolder{ID: ids.Must(), Path: &path, LibraryID: &libraryID, CreatedAt: now, UpdatedAt: now})
}

// updateRequest is the body of PATCH /api/libraries/{id}; a nil field is a key
// that was not sent. An empty text or an unchanged value changes nothing.
type updateRequest struct {
	Name         *string        `json:"name"`
	Provider     *string        `json:"provider"`
	MediaType    *string        `json:"mediaType"`
	Icon         *string        `json:"icon"`
	DisplayOrder *int64         `json:"displayOrder"`
	Settings     *settingsPatch `json:"settings"`
	Folders      *[]folderInput `json:"folders"`
}

// apply writes the sent fields onto lib and returns the columns that changed
// and whether the watcher setting is one of them.
func (req updateRequest) apply(lib *db.Library) (columns []string, watcherChanged bool) {
	podcast := db.Val(lib.MediaType) == "podcast" // the settings keys are those of the type before this edit
	for _, f := range []struct {
		sent   *string
		stored **string
		column string
	}{{req.Name, &lib.Name, "name"}, {req.Provider, &lib.Provider, "provider"}, {req.MediaType, &lib.MediaType, "mediaType"}, {req.Icon, &lib.Icon, "icon"}} {
		if f.sent != nil && *f.sent != "" && *f.sent != db.Val(*f.stored) {
			*f.stored = f.sent
			columns = append(columns, f.column)
		}
	}
	if req.DisplayOrder != nil && *req.DisplayOrder != db.Val(lib.DisplayOrder) {
		lib.DisplayOrder = req.DisplayOrder
		columns = append(columns, "displayOrder")
	}
	if req.Settings != nil {
		before := lib.Settings.V
		req.Settings.apply(&lib.Settings.V, podcast)
		if !reflect.DeepEqual(before, lib.Settings.V) {
			columns = append(columns, "settings")
			watcherChanged = before.DisableWatcher != lib.Settings.V.DisableWatcher
		}
	}
	return columns, watcherChanged
}

// update is PATCH /api/libraries/{id} (LibraryController.update). Folders not
// listed by id are removed together with their items, new ones are created.
// The watchers are refreshed once, after the commit, and only when a folder or
// the watcher setting changed.
func (h *Handler) update(w http.ResponseWriter, r *http.Request) error {
	ctx, id := r.Context(), r.PathValue("id")
	defer h.deps.Filters.Invalidate(id) // LibraryController.js:513
	if _, err := h.view(ctx, id); errors.Is(err, sql.ErrNoRows) {
		return httpx.Error(http.StatusNotFound, "Library not found")
	} else if err != nil {
		return httpx.Internal("database read failed", err)
	}
	req, err := httpx.DecodeLimit[updateRequest](w, r, bodyLimit)
	if err != nil {
		return err
	}
	var added, removed []string // new folder paths, ids of the folders to drop
	if req.Folders != nil {
		removed, err = selectAll[string](ctx, h.db, `SELECT id FROM libraryFolders WHERE libraryId = ? ORDER BY rowid`, id)
		if err != nil {
			return httpx.Internal("database read failed", err)
		}
		for _, folder := range *req.Folders {
			if folder.ID != "" {
				removed = slices.DeleteFunc(removed, func(current string) bool { return current == folder.ID })
				continue
			}
			path, err := makeFolder(folder)
			if err != nil {
				return err
			}
			added = append(added, path)
		}
	}
	for _, folderID := range removed {
		if err := h.removeFolderItems(ctx, folderID); err != nil {
			return httpx.Internal("database write failed", err)
		}
	}
	var changed, watcherChanged bool
	err = h.db.Tx(ctx, func(ctx context.Context) error {
		var lib db.Library
		if err := h.db.Get(ctx, &lib, `SELECT * FROM libraries WHERE id = ?`, id); err != nil {
			return err
		}
		columns, watcher := req.apply(&lib)
		if changed = len(columns)+len(added)+len(removed) > 0; !changed {
			return nil
		}
		watcherChanged = watcher
		lib.UpdatedAt = db.Now()
		if err := h.db.Update(ctx, "libraries", &lib, append(columns, "updatedAt")...); err != nil {
			return err
		}
		for _, path := range added {
			if err := h.insertFolder(ctx, id, path, lib.UpdatedAt); err != nil {
				return err
			}
		}
		_, err := h.db.Exec(ctx, `DELETE FROM libraryFolders WHERE id IN (?) AND libraryId = ?`, removed, id)
		return err
	})
	if err != nil {
		return httpx.Internal("database write failed", err)
	}
	view, err := h.view(ctx, id)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	if changed {
		if len(added)+len(removed) > 0 || watcherChanged {
			h.changed(id)
		}
		h.emit.ToAll("library_updated", view)
	}
	return httpx.JSON(w, r, view)
}

// reorder is POST /api/libraries/order: the body lists libraries with their new
// display order, all of which must exist or none is changed. It answers every
// library in display order.
func (h *Handler) reorder(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	req, err := httpx.DecodeLimit[[]struct {
		ID       string `json:"id"`
		NewOrder int64  `json:"newOrder"`
	}](w, r, bodyLimit)
	if err != nil {
		return httpx.BadRequest("Invalid request. Request body must be an array of objects")
	}
	err = h.db.Tx(ctx, func(ctx context.Context) error {
		for _, entry := range req {
			var lib db.Library
			if entry.ID == "" {
				return httpx.BadRequest("Invalid request. Request body must be an array of objects")
			}
			if err := h.db.Get(ctx, &lib, `SELECT * FROM libraries WHERE id = ?`, entry.ID); errors.Is(err, sql.ErrNoRows) {
				return httpx.BadRequest("Library not found with id " + entry.ID)
			} else if err != nil {
				return err
			}
			if db.Val(lib.DisplayOrder) != entry.NewOrder {
				lib.DisplayOrder, lib.UpdatedAt = &entry.NewOrder, db.Now()
				if err := h.db.Update(ctx, "libraries", &lib, "displayOrder", "updatedAt"); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil { // a 400 from inside the transaction passes through; anything else is a 500
		return err
	}
	libs, err := selectAll[db.Library](ctx, h.db, `SELECT * FROM libraries ORDER BY displayOrder`)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	views, err := h.views(ctx, libs)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	return httpx.JSON(w, r, struct {
		Libraries []View `json:"libraries"`
	}{views})
}
