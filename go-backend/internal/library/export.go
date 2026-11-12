package library

import (
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
)

// opmlEscape is escapeForXML (libs/xml/escapeForXML.js).
var opmlEscape = strings.NewReplacer("&", "&amp;", `"`, "&quot;", "'", "&apos;", "<", "&lt;", ">", "&gt;").Replace

// opmlAttribute is node-xml's attribute(): it escapes the value again, so the
// generator's own escapeForXML makes "&amp;amp;" (libs/xml/index.js:250).
func opmlAttribute(name, value string) string {
	return " " + name + `="` + opmlEscape(opmlEscape(value)) + `"`
}

type opmlPodcast struct {
	Title       *string `db:"title"`
	FeedURL     *string `db:"feedURL"`
	Description *string `db:"description"`
	Language    *string `db:"language"`
}

// opmlFile is opmlGenerator.generate (utils/generators/opmlGenerator.js): one
// outline per podcast with a feed URL, 4-space indented, no trailing newline.
// The generator reads podcast.itunesPageUrl, but the model attribute is
// itunesPageURL, so upstream never writes the htmlUrl attribute.
func opmlFile(podcasts []opmlPodcast) string {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<opml version=\"1.0\">\n    <head>\n        <title>Audiobookshelf Podcast Subscriptions</title>\n    </head>\n    <body>\n")
	for _, p := range podcasts {
		if db.Val(p.FeedURL) == "" {
			continue
		}
		title := "null" // a NULL title is written as the text "null"
		if p.Title != nil {
			title = *p.Title
		}
		b.WriteString(`        <outline type="rss"` + opmlAttribute("text", title) + opmlAttribute("title", title) + opmlAttribute("xmlUrl", *p.FeedURL))
		if db.Val(p.Description) != "" {
			b.WriteString(opmlAttribute("description", *p.Description))
		}
		if db.Val(p.Language) != "" {
			b.WriteString(opmlAttribute("language", *p.Language))
		}
		b.WriteString("/>\n")
	}
	b.WriteString("    </body>\n</opml>")
	return b.String()
}

// opml is GET /api/libraries/{id}/opml (LibraryController.getOPMLFile,
// js:1306-1327): the library's podcasts the user may see, as application/xml.
func (h *Handler) opml(w http.ResponseWriter, r *http.Request, scope Scope) error {
	clause, args := scope.User.MediaClause("b")
	podcasts, err := selectAll[opmlPodcast](r.Context(), h.db, `SELECT b.title, b.feedURL, b.description, b.language FROM podcasts b JOIN libraryItems li ON li.mediaId=b.id WHERE li.libraryId = ?`+clause+` ORDER BY li.rowid`, append([]any{scope.ID}, args...)...)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	body := opmlFile(podcasts)
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method != http.MethodHead {
		_, _ = w.Write([]byte(body)) // a client that went away is not a server error
	}
	return nil
}

// removeMetadata is POST /api/libraries/{id}/remove-metadata
// (LibraryController.removeAllMetadataFiles, js:1333-1375): an admin deletes
// the metadata.json (metadata.abs with ?ext=abs) file that the library files
// of each item list, and learns how many were found and removed.
func (h *Handler) removeMetadata(w http.ResponseWriter, r *http.Request, scope Scope) error {
	if !isAdmin(scope.User) {
		slog.Error("non-admin user attempted to remove all metadata files", "user", db.Val(scope.User.Username))
		return httpx.Forbidden()
	}
	filename := "metadata.json"
	if r.URL.Query().Get("ext") == "abs" {
		filename = "metadata.abs"
	}
	items, err := selectAll[struct {
		Files db.JSON[[]db.LibraryFile] `db:"libraryFiles"`
	}](r.Context(), h.db, `SELECT libraryFiles FROM libraryItems WHERE libraryId = ? AND (SELECT count(*) FROM json_each(libraryFiles) WHERE json_valid(libraryFiles) AND json_extract(json_each.value, "$.metadata.filename") = ?) >= 1 ORDER BY rowid`, scope.ID, filename)
	if err != nil {
		return httpx.Internal("database read failed", err)
	}
	type result struct {
		Found   int  `json:"found"`
		Removed *int `json:"removed,omitempty"`
	}
	if len(items) == 0 {
		return httpx.JSON(w, r, result{})
	}
	removed := 0
	for _, it := range items {
		for _, file := range it.Files.V {
			if file.Metadata.Filename != filename {
				continue
			}
			// fs.remove: a file that is already gone counts as removed.
			if file.Metadata.Path == "" {
				break
			}
			if err := os.RemoveAll(file.Metadata.Path); err != nil {
				slog.Error("remove metadata file", "path", file.Metadata.Path, "error", err)
			} else {
				removed++
			}
			break
		}
	}
	return httpx.JSON(w, r, result{len(items), &removed})
}
