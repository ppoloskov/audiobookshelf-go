package library_manage

import (
	"os"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// cascadeFacts counts the rows that belong to one library.
type cascadeFacts struct {
	Items, Progress, PlaylistItems, Collections, CollectionBooks, Playlists, Feeds, Folders, Libraries, Authors, Series, Sessions int
	ItemIDs                                                                                                                       []string
}

// mediaIDs selects the media item ids of a library (books and podcast episodes); it takes the library id twice.
const mediaIDs = `SELECT mediaId FROM libraryItems WHERE libraryId = ? UNION SELECT id FROM podcastEpisodes WHERE podcastId IN (SELECT mediaId FROM libraryItems WHERE libraryId = ?)`

func factsFor(t *testing.T, e *harness.Env, id string) cascadeFacts {
	t.Helper()
	f := cascadeFacts{
		Items:           e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE libraryId = ?", id),
		Progress:        e.Count(t, "SELECT COUNT(*) FROM mediaProgresses WHERE mediaItemId IN ("+mediaIDs+")", id, id),
		PlaylistItems:   e.Count(t, "SELECT COUNT(*) FROM playlistMediaItems WHERE mediaItemId IN ("+mediaIDs+")", id, id),
		Collections:     e.Count(t, "SELECT COUNT(*) FROM collections WHERE libraryId = ?", id),
		CollectionBooks: e.Count(t, "SELECT COUNT(*) FROM collectionBooks WHERE collectionId IN (SELECT id FROM collections WHERE libraryId = ?)", id),
		Playlists:       e.Count(t, "SELECT COUNT(*) FROM playlists WHERE libraryId = ?", id),
		Feeds:           e.Count(t, "SELECT COUNT(*) FROM feeds WHERE entityId IN (SELECT id FROM libraryItems WHERE libraryId = ?)", id),
		Folders:         e.Count(t, "SELECT COUNT(*) FROM libraryFolders WHERE libraryId = ?", id),
		Libraries:       e.Count(t, "SELECT COUNT(*) FROM libraries WHERE id = ?", id),
		Authors:         e.Count(t, "SELECT COUNT(*) FROM authors WHERE libraryId = ?", id),
		Series:          e.Count(t, "SELECT COUNT(*) FROM series WHERE libraryId = ?", id),
		Sessions:        e.Count(t, "SELECT COUNT(*) FROM playbackSessions WHERE libraryId = ?", id),
	}
	for _, row := range e.Rows(t, "SELECT id FROM libraryItems WHERE libraryId = ?", id) {
		f.ItemIDs = append(f.ItemIDs, row["id"].(string))
	}
	return f
}

// itemMetadataDirs returns the item ids whose metadata/items directory exists.
func itemMetadataDirs(e *harness.Env, ids []string) []string {
	var found []string
	for _, id := range ids {
		if _, err := os.Stat(filepath.Join(e.Metadata, "items", id)); err == nil {
			found = append(found, id)
		}
	}
	return found
}

// mediaFileCount counts the regular files below a directory.
func mediaFileCount(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			n++
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}
