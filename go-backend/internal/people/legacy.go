// TEMPORARY: feeds old internal/library; delete with it.

// Package people holds the author row and JSON types the old library package
// still uses for its author list, search and shelves. The author routes live
// in internal/author.
package people

import (
	"database/sql"
	"fmt"

	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/store"
)

// AuthorRecord is an authors row.
type AuthorRecord struct {
	ID, LibraryID                                 string
	Name, LastFirst, ASIN, Description, ImagePath sql.NullString
	CreatedAt, UpdatedAt                          any
}

// AuthorColumns are the columns ScanAuthor reads.
const AuthorColumns = `id,name,lastFirst,asin,description,imagePath,libraryId,createdAt,updatedAt`

// ScanAuthor reads an AuthorColumns row.
func ScanAuthor(row interface{ Scan(...any) error }) (AuthorRecord, error) {
	var a AuthorRecord
	err := row.Scan(&a.ID, &a.Name, &a.LastFirst, &a.ASIN, &a.Description, &a.ImagePath, &a.LibraryID, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}

// AuthorView is Author.toOldJSON.
type AuthorView struct {
	ID          string  `json:"id"`
	ASIN        *string `json:"asin"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
	ImagePath   *string `json:"imagePath"`
	LibraryID   string  `json:"libraryId"`
	AddedAt     int64   `json:"addedAt"`
	UpdatedAt   int64   `json:"updatedAt"`
}

// AuthorWithBooks is Author.toOldJSONExpanded(numBooks).
type AuthorWithBooks struct {
	AuthorView
	NumBooks int `json:"numBooks"`
}

// AuthorListEntry is an entry of the library author list.
type AuthorListEntry struct {
	AuthorWithBooks
	LastFirst *string `json:"lastFirst"`
}

// ToOldJSON is Author.toOldJSON.
func (a AuthorRecord) ToOldJSON() (AuthorView, error) {
	added, err := dbtime.OptionalMillis(a.CreatedAt)
	if err != nil || added == nil {
		return AuthorView{}, fmt.Errorf("author %s createdAt: %v", a.ID, err)
	}
	updated, err := dbtime.OptionalMillis(a.UpdatedAt)
	if err != nil || updated == nil {
		return AuthorView{}, fmt.Errorf("author %s updatedAt: %v", a.ID, err)
	}
	return AuthorView{
		ID: a.ID, ASIN: store.NullStringPtr(a.ASIN), Name: store.NullStringPtr(a.Name),
		Description: store.NullStringPtr(a.Description), ImagePath: store.NullStringPtr(a.ImagePath),
		LibraryID: a.LibraryID, AddedAt: *added, UpdatedAt: *updated,
	}, nil
}
