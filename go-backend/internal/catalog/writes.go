package catalog

import ()

// itemScope is the library item row every item route starts from.
type itemScope struct {
	id, libraryID, mediaType, mediaID, path string
	isFile, missing                         bool
}

// itemScopeColumns are the libraryItems columns scanItemScope reads.
const itemScopeColumns = `li.id,li.libraryId,li.mediaType,li.mediaId,li.path,COALESCE(li.isFile,0),COALESCE(li.isMissing,0)`

type rowScanner interface{ Scan(...any) error }

func scanItemScope(row rowScanner) (itemScope, error) {
	var s itemScope
	var isFile, missing int
	err := row.Scan(&s.id, &s.libraryID, &s.mediaType, &s.mediaID, &s.path, &isFile, &missing)
	s.isFile, s.missing = isFile != 0, missing != 0
	return s, err
}
