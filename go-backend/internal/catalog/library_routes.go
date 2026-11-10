// Library routes mirroring server/controllers/LibraryController.js (pinned
// v2.37.2) that were missing: the podcast OPML export, the multi-item zip
// download and the metadata file removal. Registered on the ServeMux by Routes.
package catalog

import (
	"archive/zip"
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/library"
	"audiobookshelf-compat/internal/logging"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// opmlEscape is escapeForXML (libs/xml/escapeForXML.js).
func opmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", `"`, "&quot;", "'", "&apos;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// opmlAttribute is node-xml's attribute(): the value is escaped again, so the
// generator's own escapeForXML makes "&amp;amp;" (libs/xml/index.js:250).
func opmlAttribute(name, value string) string { return name + `="` + opmlEscape(value) + `"` }

type opmlPodcast struct {
	title, feedURL, description, language sql.NullString
}

// generateOPML is opmlGenerator.generate (utils/generators/opmlGenerator.js):
// one outline per podcast that has a feed URL, 4-space indented, no trailing
// newline.
func generateOPML(podcasts []opmlPodcast) string {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<opml version=\"1.0\">\n    <head>\n        <title>Audiobookshelf Podcast Subscriptions</title>\n    </head>\n    <body>\n")
	for _, p := range podcasts {
		if !p.feedURL.Valid || p.feedURL.String == "" {
			continue
		}
		title := "null"
		if p.title.Valid {
			title = opmlEscape(p.title.String)
		}
		attrs := []string{`type="rss"`, opmlAttribute("text", title), opmlAttribute("title", title), opmlAttribute("xmlUrl", opmlEscape(p.feedURL.String))}
		if p.description.Valid && p.description.String != "" {
			attrs = append(attrs, opmlAttribute("description", opmlEscape(p.description.String)))
		}
		// The generator reads podcast.itunesPageUrl, but the model attribute is
		// itunesPageURL, so upstream never writes the htmlUrl attribute.
		if p.language.Valid && p.language.String != "" {
			attrs = append(attrs, opmlAttribute("language", opmlEscape(p.language.String)))
		}
		b.WriteString("        <outline " + strings.Join(attrs, " ") + "/>\n")
	}
	b.WriteString("    </body>\n</opml>")
	return b.String()
}

// libraryOPML is LibraryController.getOPMLFile (LibraryController.js:1306-1327):
// the library's podcasts the user may see, as application/xml.
func (h *Handler) libraryOPML(w http.ResponseWriter, r *http.Request, library library.Scope) error {
	user := library.User
	permissionSQL, permissionArgs := access.For(r.Context(), user).MediaClause("b")
	rows, err := h.db.QueryContext(r.Context(), `SELECT b.title,b.feedURL,b.description,b.language FROM podcasts b JOIN libraryItems li ON li.mediaId=b.id WHERE li.libraryId=?`+permissionSQL+` ORDER BY li.rowid`, append([]any{library.ID}, permissionArgs...)...)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	defer rows.Close()
	var podcasts []opmlPodcast
	for rows.Next() {
		var p opmlPodcast
		if err := rows.Scan(&p.title, &p.feedURL, &p.description, &p.language); err != nil {
			return httputil.Internal("database read failed", err)
		}
		podcasts = append(podcasts, p)
	}
	body := []byte(generateOPML(podcasts))
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("ETag", httputil.ExpressETag(body))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
	return nil
}

// libraryRemoveMetadata is LibraryController.removeAllMetadataFiles
// (LibraryController.js:1333-1375): admins delete the metadata.json (or, with
// ext=abs, metadata.abs) file of every item whose library files list one.
func (h *Handler) libraryRemoveMetadata(w http.ResponseWriter, r *http.Request, library library.Scope) error {
	user := library.User
	if !adminUser(user) {
		logging.Errorf(`[LibraryController] Non-admin user "%s" attempted to remove all metadata files`, user.Username)
		return httputil.Status(http.StatusForbidden, "")
	}
	ext := "json"
	if r.URL.Query().Get("ext") == "abs" {
		ext = "abs"
	}
	filename := "metadata." + ext
	rows, err := h.db.QueryContext(r.Context(), `SELECT libraryFiles FROM libraryItems WHERE libraryId=? AND (SELECT count(*) FROM json_each(libraryFiles) WHERE json_valid(libraryFiles) AND json_extract(json_each.value, "$.metadata.filename") = ?) >= 1 ORDER BY rowid`, library.ID, filename)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	var paths []string
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return httputil.Internal("database read failed", err)
		}
		var files []struct {
			Metadata struct {
				Filename string `json:"filename"`
				Path     string `json:"path"`
			} `json:"metadata"`
		}
		_ = json.Unmarshal(raw, &files)
		path := ""
		for _, file := range files {
			if file.Metadata.Filename == filename {
				path = file.Metadata.Path
				break
			}
		}
		paths = append(paths, path)
	}
	if err := rows.Close(); err != nil {
		return httputil.Internal("database read failed", err)
	}
	if len(paths) == 0 {
		logging.Infof("[LibraryController] No %s files found to remove", filename)
		return httputil.JSON(w, r, struct {
			Found int `json:"found"`
		}{0})
	}
	logging.Infof("[LibraryController] Found %d %s files to remove", len(paths), filename)
	removed := 0
	for _, path := range paths {
		if path == "" {
			continue
		}
		logging.Debugf(`[LibraryController] Removing file "%s"`, path)
		// fileUtils.removeFile is fs.remove: a file that is already gone counts.
		if err := os.RemoveAll(path); err != nil {
			logging.Errorf(`[fileUtils] Failed remove file "%s" %v`, path, err)
			continue
		}
		removed++
	}
	return httputil.JSON(w, r, struct {
		Found   int `json:"found"`
		Removed int `json:"removed"`
	}{len(paths), removed})
}

// libraryDownload is LibraryController.downloadMultiple
// (LibraryController.js:1422-1465): the items named by ?ids (comma separated)
// of this library as one zip, each item under its folder name (a single-file
// item as that file), stored without compression.
func (h *Handler) libraryDownload(w http.ResponseWriter, r *http.Request, library library.Scope) error {
	user := library.User
	if !access.For(r.Context(), user).Download() {
		logging.Warnf(`User "%s" attempted to download without permission`, user.Username)
		return httputil.Status(http.StatusForbidden, "")
	}
	idsParam, present := r.URL.Query()["ids"]
	if !present || len(idsParam) != 1 || idsParam[0] == "" {
		return httputil.Status(http.StatusBadRequest, "Invalid request. ids must be a string")
	}
	itemIDs := strings.Split(idsParam[0], ",")
	ctx := r.Context()
	found, err := h.itemsExpandedWhere(ctx, itemIDs)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	var items []itemScope
	for _, item := range found {
		if item.libraryID == library.ID {
			items = append(items, item)
		}
	}
	allowed, err := h.userCanAccessItems(ctx, user, items)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	if !allowed {
		return httputil.Status(http.StatusForbidden, "")
	}
	if len(items) < len(itemIDs) {
		logging.Warnf(`[LibraryController] User "%s" requested %d items but only %d are in library "%s"`, user.Username, len(itemIDs), len(items), library.ID)
	}
	logging.Infof(`[LibraryController] User "%s" requested download for items "%s"`, user.Username, strings.Join(itemIDs, ","))
	filename := fmt.Sprintf("LibraryItems-%d.zip", time.Now().UnixMilli())
	if len(items) == 0 {
		logging.Warnf(`[LibraryController] No library items found for ids "%s"`, strings.Join(itemIDs, ","))
		return httputil.Status(http.StatusNotFound, "Library items not found")
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	// Streamed without a Content-Length, like archiver piped into the response.
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	zw := zip.NewWriter(w)
	for _, item := range items {
		var err error
		if item.isFile {
			err = addFileToZip(zw, item.path, filepath.Base(item.path))
		} else {
			err = addTreeToZip(zw, item.path, filepath.Base(item.path))
		}
		if err != nil {
			// Headers are sent; upstream logs the archiver error and the
			// response ends (zipHelpers.js:73-79).
			logging.Errorf(`[DownloadManager] Archiver error: %v`, err)
			break
		}
	}
	_ = zw.Close()
	logging.Infof(`[LibraryController] Downloaded %d items "%s"`, len(items), filename)
	return nil
}

func zipHeader(name string, info fs.FileInfo) *zip.FileHeader {
	header := &zip.FileHeader{Name: name, Method: zip.Store, Modified: info.ModTime()}
	header.SetMode(info.Mode())
	return header
}

func addFileToZip(zw *zip.Writer, path, name string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	target, err := zw.CreateHeader(zipHeader(name, info))
	if err != nil {
		return err
	}
	source, err := os.Open(path)
	if err != nil {
		return err
	}
	defer source.Close()
	_, err = io.Copy(target, source)
	return err
}

// addTreeToZip is archiver's directory(path, name): every file and every
// directory below path (as "name/rel/") under the name prefix.
func addTreeToZip(zw *zip.Writer, root, name string) error {
	return filepath.WalkDir(root, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		entryName := name + "/" + filepath.ToSlash(rel)
		if entry.IsDir() {
			_, err := zw.CreateHeader(zipHeader(entryName+"/", info))
			return err
		}
		return addFileToZip(zw, p, entryName)
	})
}
