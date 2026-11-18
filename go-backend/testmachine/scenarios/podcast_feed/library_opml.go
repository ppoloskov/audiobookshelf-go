package podcast_feed

import (
	"net/http"
	"strings"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	libraryOPMLReq     = "route-r040"
	libraryOPMLInv     = "INV-R040-OPML-001"
	libraryOPMLPermInv = "INV-R040-PERM-001"
	libraryOPMLAuthInv = "INV-R040-SAMPLED"
	libraryOPMLSrc     = "server/controllers/LibraryController.js:1306-1324"
	libraryOPMLGenSrc  = "server/utils/generators/opmlGenerator.js:1-50"
)

func libraryOPMLScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: libraryOPMLReq, Invariant: libraryOPMLInv, Case: "normal", State: "stateless", Assertion: "library-opml-lists-podcast-feeds",
			Sources: []string{libraryOPMLSrc, libraryOPMLGenSrc},
			Claim:   "GET /api/libraries/:id/opml answers an OPML 1.0 document that names the library's podcast subscriptions as rss outlines with type, text, title, xmlUrl, the escaped description and the language when the podcast has one, never writes htmlUrl, and omits every podcast without a feed URL.",
			Run: func(t *testing.T, e *harness.Env) {
				resp, doc := opmlOf(t, e, e.Token(t, "root"), e.IDs["podcastLibrary"])
				requireValue(t, "content type", resp.Header.Get("Content-Type"), "application/xml; charset=utf-8")
				if doc.Version != "1.0" || doc.Head.Title != "Audiobookshelf Podcast Subscriptions" {
					t.Fatalf("document head = %q %q", doc.Version, doc.Head.Title)
				}
				outlines := outlinesByURL(doc)
				_, folderPath := libraryFolder(t, e, e.IDs["podcastLibrary"])
				signal := itemOfByPath(t, e, folderPath+"/Signal Show")
				wire := itemOfByPath(t, e, folderPath+"/Wire Signal")
				explicit := itemOfByPath(t, e, folderPath+"/Explicit Show")
				tagged := itemOfByPath(t, e, folderPath+"/Tagged Show")
				night := itemOfByPath(t, e, folderPath+"/Night Wire")
				if len(outlines) != 6 {
					t.Fatalf("%d outlines, want 6: %+v", len(outlines), outlines)
				}
				for _, item := range []libraryItemJSON{signal, wire, explicit, tagged, night, itemOfByPath(t, e, folderPath+"/Fresh Wire")} {
					feedURL := strPtr(item.Media.Metadata.FeedURL)
					outline, ok := outlines[feedURL]
					if !ok {
						t.Fatalf("no outline for %s (%s)", strPtr(item.Media.Metadata.Title), feedURL)
					}
					if outline.Type != "rss" || outline.Text != strPtr(item.Media.Metadata.Title) || outline.Title != outline.Text || outline.XMLURL != feedURL {
						t.Fatalf("outline for %s = %+v", feedURL, outline)
					}
				}
				wantSignal := outlines[strPtr(signal.Media.Metadata.FeedURL)]
				// The generator escapes the description and the XML serializer escapes it
				// again (server/utils/generators/opmlGenerator.js:23-25 escapeForXML), so the
				// parsed attribute carries the escaped HTML.
				requireValue(t, "signal description", wantSignal.Description, "&lt;p&gt;Signal &lt;b&gt;description&lt;/b&gt;&lt;/p&gt;")
				// opmlGenerator reads podcast.itunesPageUrl although the model attribute is
				// itunesPageURL, so the htmlUrl attribute is never written.
				requireValue(t, "signal htmlUrl (never written)", wantSignal.HTMLURL, "")
				requireValue(t, "signal language", wantSignal.Language, "en-us")
				requireValue(t, "night description", outlines[strPtr(night.Media.Metadata.FeedURL)].Description, "")
				requireValue(t, "night language", outlines[strPtr(night.Media.Metadata.FeedURL)].Language, "")
				if _, ok := outlines["http://127.0.0.1:1/unused"]; ok {
					t.Fatal("an unexpected outline")
				}
				listed := map[string]bool{}
				for _, outline := range doc.Body.Outlines {
					listed[outline.Text] = true
				}
				for _, title := range []string{"Fixture Cast", "Minimal Show"} {
					if listed[title] {
						t.Fatalf("%s has no feed URL but appears in the OPML", title)
					}
				}
			},
		},
		{
			Requirement: libraryOPMLReq, Invariant: libraryOPMLInv, Case: "edge", State: "stateless", Assertion: "library-opml-empty-for-book-library",
			Sources: []string{libraryOPMLSrc, libraryOPMLGenSrc},
			Claim:   "A library without podcasts answers a well-formed OPML document with an empty body instead of an error.",
			Run: func(t *testing.T, e *harness.Env) {
				_, doc := opmlOf(t, e, e.Token(t, "root"), e.IDs["library"])
				if len(doc.Body.Outlines) != 0 || doc.Head.Title != "Audiobookshelf Podcast Subscriptions" {
					t.Fatalf("book library OPML = %+v", doc)
				}
			},
		},
		{
			Requirement: libraryOPMLReq, Invariant: libraryOPMLPermInv, Case: "edge", State: "stateless", Assertion: "library-opml-respects-explicit-and-tag-permissions",
			Sources: []string{libraryOPMLSrc, "server/utils/queries/libraryItemsPodcastFilters.js:12-44", "server/models/User.js:646-683"},
			Claim:   "The OPML file honours the caller's content permissions: a user without access to explicit content does not receive the explicit podcast, and a user whose selected tags are not accessible does not receive a podcast carrying that tag, while both keep the other subscriptions.",
			Run: func(t *testing.T, e *harness.Env) {
				_, rootDoc := opmlOf(t, e, e.Token(t, "root"), e.IDs["podcastLibrary"])
				all := len(rootDoc.Body.Outlines)
				_, finisherDoc := opmlOf(t, e, e.Token(t, "finisher"), e.IDs["podcastLibrary"])
				if len(finisherDoc.Body.Outlines) != all-1 {
					t.Fatalf("a user without explicit access sees %d outlines, want %d: %+v", len(finisherDoc.Body.Outlines), all-1, finisherDoc.Body.Outlines)
				}
				for _, outline := range finisherDoc.Body.Outlines {
					if outline.Text == "Explicit Show" {
						t.Fatal("an explicit podcast was listed for a user without explicit access")
					}
				}
				tagUser := createTagRestrictedUser(t, e)
				_, tagDoc := opmlOf(t, e, tagUser, e.IDs["podcastLibrary"])
				if len(tagDoc.Body.Outlines) != all-1 {
					t.Fatalf("a tag-restricted user sees %d outlines, want %d: %+v", len(tagDoc.Body.Outlines), all-1, tagDoc.Body.Outlines)
				}
				var seen []string
				for _, outline := range tagDoc.Body.Outlines {
					seen = append(seen, outline.Text)
					if outline.Text == "Tagged Show" {
						t.Fatal("a podcast with the hidden tag was listed")
					}
				}
				if !strings.Contains(strings.Join(seen, ","), "Explicit Show") {
					t.Fatalf("the tag-restricted user must still see the explicit podcast: %v", seen)
				}
			},
		},
		{
			Requirement: libraryOPMLReq, Invariant: libraryOPMLAuthInv, Case: "error", State: "stateless", Assertion: "library-opml-rejections",
			Sources: []string{libraryOPMLSrc, "server/controllers/LibraryController.js:1475-1500"},
			Claim:   "GET /api/libraries/:id/opml answers 401 without a token, 403 for an admin without access to the library and 404 for an unknown library id.",
			Run: func(t *testing.T, e *harness.Env) {
				restricted := createAdmin(t, e, "opml-restricted-admin", e.IDs["library"])
				wantStatus(t, "anonymous", e.Get(t, "", "/api/libraries/"+e.IDs["podcastLibrary"]+"/opml"), http.StatusUnauthorized)
				wantStatus(t, "admin of another library", e.Get(t, restricted, "/api/libraries/"+e.IDs["podcastLibrary"]+"/opml"), http.StatusForbidden)
				wantStatus(t, "unknown library", e.Get(t, e.Token(t, "root"), "/api/libraries/not-a-library/opml"), http.StatusNotFound)
			},
		},
	}
}

// createTagRestrictedUser creates a user who may not see podcasts carrying the
// "secret" tag but may access explicit content.
func createTagRestrictedUser(t *testing.T, e *harness.Env) string {
	t.Helper()
	return createUser(t, e, "opml-tag-user", map[string]any{
		"permissions": map[string]any{
			"accessAllTags":             false,
			"selectedTagsNotAccessible": true,
			"itemTagsSelected":          []string{"secret"},
			"accessExplicitContent":     true,
		},
	})
}
