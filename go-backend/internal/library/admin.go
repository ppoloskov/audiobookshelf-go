package library

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"

	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/logging"
)

// opmlEscape is escapeForXML (libs/xml/escapeForXML.js).
func opmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", `"`, "&quot;", "'", "&apos;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// opmlAttribute is node-xml's attribute(): the value is escaped again, so the
// generator's own escapeForXML makes "&amp;amp;" (libs/xml/index.js:250).
func opmlAttribute(name, value string) string { return name + `="` + opmlEscape(value) + `"` }

type OPMLPodcast struct {
	Title, FeedURL, Description, Language sql.NullString
}

// generateOPML is opmlGenerator.generate (utils/generators/opmlGenerator.js):
// one outline per podcast that has a feed URL, 4-space indented, no trailing
// newline.
func GenerateOPML(podcasts []OPMLPodcast) string {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<opml version=\"1.0\">\n    <head>\n        <title>Audiobookshelf Podcast Subscriptions</title>\n    </head>\n    <body>\n")
	for _, p := range podcasts {
		if !p.FeedURL.Valid || p.FeedURL.String == "" {
			continue
		}
		title := "null"
		if p.Title.Valid {
			title = opmlEscape(p.Title.String)
		}
		attrs := []string{`type="rss"`, opmlAttribute("text", title), opmlAttribute("title", title), opmlAttribute("xmlUrl", opmlEscape(p.FeedURL.String))}
		if p.Description.Valid && p.Description.String != "" {
			attrs = append(attrs, opmlAttribute("description", opmlEscape(p.Description.String)))
		}
		// The generator reads podcast.itunesPageUrl, but the model attribute is
		// itunesPageURL, so upstream never writes the htmlUrl attribute.
		if p.Language.Valid && p.Language.String != "" {
			attrs = append(attrs, opmlAttribute("language", opmlEscape(p.Language.String)))
		}
		b.WriteString("        <outline " + strings.Join(attrs, " ") + "/>\n")
	}
	b.WriteString("    </body>\n</opml>")
	return b.String()
}

// libraryOPML is LibraryController.getOPMLFile (LibraryController.js:1306-1327):
// the library's podcasts the user may see, as application/xml.
func (h *Handler) libraryOPML(w http.ResponseWriter, r *http.Request, library Scope) error {
	user := library.User
	permissionSQL, permissionArgs := access.For(r.Context(), user).MediaClause("b")
	rows, err := h.db.QueryContext(r.Context(), `SELECT b.title,b.feedURL,b.description,b.language FROM podcasts b JOIN libraryItems li ON li.mediaId=b.id WHERE li.libraryId=?`+permissionSQL+` ORDER BY li.rowid`, append([]any{library.ID}, permissionArgs...)...)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	defer rows.Close()
	var podcasts []OPMLPodcast
	for rows.Next() {
		var p OPMLPodcast
		if err := rows.Scan(&p.Title, &p.FeedURL, &p.Description, &p.Language); err != nil {
			return httputil.Internal("database read failed", err)
		}
		podcasts = append(podcasts, p)
	}
	body := []byte(GenerateOPML(podcasts))
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
func (h *Handler) libraryRemoveMetadata(w http.ResponseWriter, r *http.Request, library Scope) error {
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
