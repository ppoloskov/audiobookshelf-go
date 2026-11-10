package library

import (
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/store"
	"context"
	"database/sql"
)

// FolderView is Folder.toOldJSON.
type FolderView struct {
	ID        string `json:"id"`
	FullPath  string `json:"fullPath"`
	LibraryID string `json:"libraryId"`
	AddedAt   *int64 `json:"addedAt"`
}

// View is Library.toOldJSON (server/models/Library.js), the one shape of a
// library in lists, details and the create, update and order responses.
type View struct {
	ID              string         `json:"id"`
	Name            string         `json:"name"`
	Folders         []FolderView   `json:"folders"`
	DisplayOrder    int64          `json:"displayOrder"`
	Icon            *string        `json:"icon"`
	MediaType       *string        `json:"mediaType"`
	Provider        *string        `json:"provider"`
	Settings        map[string]any `json:"settings"`
	LastScan        *int64         `json:"lastScan"`
	LastScanVersion *string        `json:"lastScanVersion"`
	CreatedAt       *int64         `json:"createdAt"`
	LastUpdate      *int64         `json:"lastUpdate"`
	Stats           any            `json:"stats,omitempty"`
}

// View reads a library with its folders. lastScan is
// `lastScan?.valueOf() || null`, so a zero scan time is null.
func ReadView(ctx context.Context, db *sql.DB, id string) (View, error) {
	v := View{ID: id, Folders: []FolderView{}}
	var name, icon, mediaType, provider, version sql.NullString
	var order sql.NullInt64
	var lastScan, created, updated any
	var settings []byte
	if err := db.QueryRowContext(ctx, `SELECT name,displayOrder,icon,mediaType,provider,lastScan,lastScanVersion,settings,createdAt,updatedAt FROM libraries WHERE id=?`, id).
		Scan(&name, &order, &icon, &mediaType, &provider, &lastScan, &version, &settings, &created, &updated); err != nil {
		return v, err
	}
	v.Name, v.DisplayOrder, v.Settings = name.String, order.Int64, store.DecodeJSONObject(settings)
	v.Icon, v.MediaType, v.Provider, v.LastScanVersion = store.NullStringPtr(icon), store.NullStringPtr(mediaType), store.NullStringPtr(provider), store.NullStringPtr(version)
	var err error
	if v.LastScan, err = dbtime.OptionalMillis(lastScan); err != nil {
		return v, err
	}
	if v.LastScan != nil && *v.LastScan == 0 {
		v.LastScan = nil
	}
	if v.CreatedAt, err = dbtime.OptionalMillis(created); err != nil {
		return v, err
	}
	if v.LastUpdate, err = dbtime.OptionalMillis(updated); err != nil {
		return v, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id,path,createdAt FROM libraryFolders WHERE libraryId=? ORDER BY createdAt,rowid`, id)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		f := FolderView{LibraryID: id}
		var added any
		if err := rows.Scan(&f.ID, &f.FullPath, &added); err != nil {
			return v, err
		}
		if f.AddedAt, err = dbtime.OptionalMillis(added); err != nil {
			return v, err
		}
		v.Folders = append(v.Folders, f)
	}
	return v, rows.Err()
}
