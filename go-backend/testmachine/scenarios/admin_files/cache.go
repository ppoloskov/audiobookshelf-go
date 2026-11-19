package admin_files

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	cachePurgePath = "/api/cache/purge"
	cacheItemsPath = "/api/cache/items/purge"

	cacheRoute      = "server/routers/ApiRouter.js:296"
	cacheItemsRoute = "server/routers/ApiRouter.js:297"
	cacheSrc        = "server/controllers/CacheController.js:20-26"
	cacheItemsSrc   = "server/controllers/CacheController.js:34-40"
	cacheManagerSrc = "server/managers/CacheManager.js:25-42,128-146"
)

// cacheSubdirs are the directories CacheManager.ensureCachePaths creates below
// metadata/cache on every purge.
var cacheSubdirs = []string{"covers", "images", "items"}

// cacheEntries lists metadata/cache as sorted slash paths; a directory keeps a
// trailing slash so an empty re-created directory is visible.
func cacheEntries(t *testing.T, e *harness.Env) []string {
	t.Helper()
	root := filepath.Join(e.Metadata, "cache")
	var entries []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			rel += "/"
		}
		entries = append(entries, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("read cache directory: %v", err)
	}
	slices.Sort(entries)
	return entries
}

// wantCacheEntries asserts the exact cache tree below metadata/cache.
func wantCacheEntries(t *testing.T, e *harness.Env, what string, want ...string) {
	t.Helper()
	slices.Sort(want)
	sameList(t, what, cacheEntries(t, e), want)
}

// resetCache clears metadata/cache and recreates the three empty directories,
// so each purge scenario starts from a known cache.
func resetCache(t *testing.T, e *harness.Env) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(e.Metadata, "cache")); err != nil {
		t.Fatal(err)
	}
	for _, sub := range cacheSubdirs {
		if err := os.MkdirAll(filepath.Join(e.Metadata, "cache", sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// seedCacheFile writes a cache entry the way the cover, image and item caches do.
func seedCacheFile(t *testing.T, e *harness.Env, sub, name string) {
	t.Helper()
	dir := filepath.Join(e.Metadata, "cache", sub)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("cache probe "+name), 0o644); err != nil {
		t.Fatal(err)
	}
}

// subtreeFiles lists the files below a directory as sorted relative slash paths.
func subtreeFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read %s: %v", root, err)
	}
	slices.Sort(files)
	return files
}

// purge posts one of the purge routes as an admin and requires 200.
func purge(t *testing.T, e *harness.Env, path string) {
	t.Helper()
	wantStatus(t, "POST "+path, e.Post(t, e.Token(t, "root"), path, nil), http.StatusOK)
}

func cacheScenarios() []harness.Scenario {
	allDirs := []string{"covers/", "images/", "items/"}
	return []harness.Scenario{
		scenario("route-r181", "INV-ROUTE-R181-PURGE", "normal", "initial", "cache-purge-removes-the-whole-cache",
			"POST /api/cache/purge as an admin answers 200, deletes every cached cover, image and item file together with the cache directory, and recreates the empty covers, images and items directories while item metadata and library rows stay as they were.",
			[]string{cacheRoute, cacheSrc, cacheManagerSrc}, func(t *testing.T, e *harness.Env) {
				resetCache(t, e)
				seedCacheFile(t, e, "covers", "item_400.webp")
				seedCacheFile(t, e, "images", "author_400.webp")
				seedCacheFile(t, e, "items", "item.cache")
				seedCacheFile(t, e, "", "root-probe")
				itemsBefore := subtreeFiles(t, filepath.Join(e.Metadata, "items"))
				rowsBefore := e.Rows(t, "SELECT id, size, mediaType FROM libraryItems ORDER BY id")
				purge(t, e, cachePurgePath)
				wantCacheEntries(t, e, "cache after a full purge", allDirs...)
				sameList(t, "item metadata files after a purge", subtreeFiles(t, filepath.Join(e.Metadata, "items")), itemsBefore)
				if after := e.Rows(t, "SELECT id, size, mediaType FROM libraryItems ORDER BY id"); !sameRows(after, rowsBefore) {
					t.Fatalf("library items changed: %v", after)
				}
			}),
		scenario("route-r181", "INV-ROUTE-R181-PURGE", "edge", "initial", "cache-purge-recreates-a-missing-cache-directory",
			"With metadata/cache missing entirely, POST /api/cache/purge answers 200 and creates the cache directory with empty covers, images and items directories.",
			[]string{cacheRoute, cacheSrc, cacheManagerSrc}, func(t *testing.T, e *harness.Env) {
				resetCache(t, e)
				if err := os.RemoveAll(filepath.Join(e.Metadata, "cache")); err != nil {
					t.Fatal(err)
				}
				purge(t, e, cachePurgePath)
				wantCacheEntries(t, e, "cache recreated after a purge", allDirs...)
			}),
		scenario("route-r181", "INV-ROUTE-R181-REPEAT", "normal", "repeat", "cache-purge-repeat-is-idempotent",
			"A second and third POST /api/cache/purge answer 200, remove cache entries written between the calls, and leave the same empty covers, images and items directories.",
			[]string{cacheRoute, cacheSrc, cacheManagerSrc}, func(t *testing.T, e *harness.Env) {
				resetCache(t, e)
				seedCacheFile(t, e, "covers", "second.webp")
				seedCacheFile(t, e, "items", "second.cache")
				purge(t, e, cachePurgePath)
				wantCacheEntries(t, e, "cache after the second purge", allDirs...)
				purge(t, e, cachePurgePath)
				wantCacheEntries(t, e, "cache after the third purge", allDirs...)
			}),
		scenario("route-r181", "INV-ROUTE-R181-PURGE", "normal", "changed", "cache-purge-removes-what-the-items-purge-keeps",
			"After POST /api/cache/items/purge has emptied only the item cache, POST /api/cache/purge removes the cover and image entries it kept and recreates all three directories.",
			[]string{cacheRoute, cacheSrc, cacheItemsRoute, cacheItemsSrc, cacheManagerSrc}, func(t *testing.T, e *harness.Env) {
				resetCache(t, e)
				seedCacheFile(t, e, "covers", "kept.webp")
				seedCacheFile(t, e, "images", "kept.webp")
				seedCacheFile(t, e, "items", "gone.cache")
				purge(t, e, cacheItemsPath)
				wantCacheEntries(t, e, "cache after the item purge", "covers/", "covers/kept.webp", "images/", "images/kept.webp", "items/")
				purge(t, e, cachePurgePath)
				wantCacheEntries(t, e, "cache after the full purge", allDirs...)
			}),
		scenario("route-r181", "INV-ROUTE-R181-ROUTE", "error", "failure", "cache-purge-denied-changes-nothing",
			"POST /api/cache/purge answers 401 without a token and 403 to the non-admin fixture user, and the cache files, database rows and metadata stay exactly as they were.",
			[]string{cacheRoute, cacheSrc}, func(t *testing.T, e *harness.Env) {
				resetCache(t, e)
				seedCacheFile(t, e, "covers", "survivor.webp")
				seedCacheFile(t, e, "items", "survivor.cache")
				_ = e.Token(t, "listener")
				_ = e.Token(t, "root")
				verifyUnchanged := unchanged(t, e)
				defer verifyUnchanged()
				anonymousAndListener(t, e, "POST", cachePurgePath, nil)
				wantCacheEntries(t, e, "cache after the rejected purges", "covers/", "covers/survivor.webp", "images/", "items/", "items/survivor.cache")
			}),
		scenario("route-r182", "INV-ROUTE-R182-PURGE", "normal", "initial", "items-purge-keeps-cover-and-image-cache",
			"POST /api/cache/items/purge as an admin answers 200, deletes the cached item files and recreates the empty items directory, while cached covers, cached images, item metadata and library rows stay as they were.",
			[]string{cacheItemsRoute, cacheItemsSrc, cacheManagerSrc}, func(t *testing.T, e *harness.Env) {
				resetCache(t, e)
				seedCacheFile(t, e, "covers", "item_400.webp")
				seedCacheFile(t, e, "images", "author_400.webp")
				seedCacheFile(t, e, "items", "item.cache")
				itemsBefore := subtreeFiles(t, filepath.Join(e.Metadata, "items"))
				rowsBefore := e.Rows(t, "SELECT id, size, mediaType FROM libraryItems ORDER BY id")
				purge(t, e, cacheItemsPath)
				wantCacheEntries(t, e, "cache after an item purge", "covers/", "covers/item_400.webp", "images/", "images/author_400.webp", "items/")
				sameList(t, "item metadata files after an item purge", subtreeFiles(t, filepath.Join(e.Metadata, "items")), itemsBefore)
				if after := e.Rows(t, "SELECT id, size, mediaType FROM libraryItems ORDER BY id"); !sameRows(after, rowsBefore) {
					t.Fatalf("library items changed: %v", after)
				}
			}),
		scenario("route-r182", "INV-ROUTE-R182-PURGE", "edge", "initial", "items-purge-recreates-a-missing-items-directory",
			"With metadata/cache/items missing, POST /api/cache/items/purge answers 200 and recreates the empty items directory without touching cached covers or images.",
			[]string{cacheItemsRoute, cacheItemsSrc, cacheManagerSrc}, func(t *testing.T, e *harness.Env) {
				resetCache(t, e)
				seedCacheFile(t, e, "covers", "kept.webp")
				if err := os.RemoveAll(filepath.Join(e.Metadata, "cache", "items")); err != nil {
					t.Fatal(err)
				}
				purge(t, e, cacheItemsPath)
				wantCacheEntries(t, e, "cache after an item purge", "covers/", "covers/kept.webp", "images/", "items/")
			}),
		scenario("route-r182", "INV-ROUTE-R182-REPEAT", "normal", "repeat", "items-purge-repeat-is-idempotent",
			"Two POST /api/cache/items/purge calls answer 200, the second removes an item cache file written between them, and cached covers and images remain.",
			[]string{cacheItemsRoute, cacheItemsSrc, cacheManagerSrc}, func(t *testing.T, e *harness.Env) {
				resetCache(t, e)
				seedCacheFile(t, e, "covers", "kept.webp")
				seedCacheFile(t, e, "items", "second.cache")
				purge(t, e, cacheItemsPath)
				purge(t, e, cacheItemsPath)
				wantCacheEntries(t, e, "cache after two item purges", "covers/", "covers/kept.webp", "images/", "items/")
			}),
		scenario("route-r182", "INV-ROUTE-R182-PURGE", "normal", "changed", "items-purge-follows-entries-written-later",
			"An item cache file written after an item purge is removed by the next POST /api/cache/items/purge, which still leaves the cached covers alone.",
			[]string{cacheItemsRoute, cacheItemsSrc, cacheManagerSrc}, func(t *testing.T, e *harness.Env) {
				resetCache(t, e)
				seedCacheFile(t, e, "covers", "kept.webp")
				seedCacheFile(t, e, "items", "first.cache")
				purge(t, e, cacheItemsPath)
				seedCacheFile(t, e, "items", "second.cache")
				purge(t, e, cacheItemsPath)
				wantCacheEntries(t, e, "cache after the second item purge", "covers/", "covers/kept.webp", "images/", "items/")
			}),
		scenario("route-r182", "INV-ROUTE-R182-ROUTE", "error", "failure", "items-purge-denied-changes-nothing",
			"POST /api/cache/items/purge answers 401 without a token and 403 to the non-admin fixture user, and the cache files, database rows and metadata stay exactly as they were.",
			[]string{cacheItemsRoute, cacheItemsSrc}, func(t *testing.T, e *harness.Env) {
				resetCache(t, e)
				seedCacheFile(t, e, "covers", "survivor.webp")
				seedCacheFile(t, e, "items", "survivor.cache")
				_ = e.Token(t, "listener")
				_ = e.Token(t, "root")
				verifyUnchanged := unchanged(t, e)
				defer verifyUnchanged()
				anonymousAndListener(t, e, "POST", cacheItemsPath, nil)
				wantCacheEntries(t, e, "cache after the rejected purges", "covers/", "covers/survivor.webp", "images/", "items/", "items/survivor.cache")
			}),
	}
}

// sameRows reports whether two row sets are identical.
func sameRows(a, b []map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for key, value := range a[i] {
			if b[i][key] != value {
				return false
			}
		}
	}
	return true
}
