package feeds

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// matchFeedID matches a feed event whose payload id is id.
func matchFeedID(id string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		if len(args) != 1 {
			return false
		}
		var value struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(args[0], &value); err != nil {
			return false
		}
		return value.ID == id
	}
}

// feedAudience is one root socket, one non-admin socket and one unauthenticated
// socket, each with the mark taken when the audience was created.
type feedAudience struct {
	admin    *harness.Socket
	listener *harness.Socket
	anon     *harness.Socket
	marks    map[*harness.Socket]int
}

// listenFeedEvents connects the three sockets every feed event is judged against.
func listenFeedEvents(t *testing.T, e *harness.Env) *feedAudience {
	t.Helper()
	a := &feedAudience{
		admin:    e.Socket(t, e.Token(t, "root")),
		listener: e.Socket(t, e.Token(t, "listener")),
		anon:     e.Socket(t, ""),
		marks:    map[*harness.Socket]int{},
	}
	a.reset()
	return a
}

// reset marks the current position of every socket.
func (a *feedAudience) reset() {
	for _, socket := range []*harness.Socket{a.admin, a.listener, a.anon} {
		a.marks[socket] = socket.Mark()
	}
}

// expect waits for the event on every authenticated socket, requires identical
// payloads and returns the decoded payload.
func (a *feedAudience) expect(t *testing.T, name, id string) feedMinJSON {
	t.Helper()
	adminEvent := a.admin.Wait(t, a.marks[a.admin], name, eventWait, matchFeedID(id))
	payload := eventPayload(t, adminEvent.Args)
	listenerEvent := a.listener.Wait(t, a.marks[a.listener], name, eventWait, matchFeedID(id))
	other := eventPayload(t, listenerEvent.Args)
	if !reflect.DeepEqual(payload, other) {
		t.Fatalf("admin and listener received different %s payloads: %+v and %+v", name, payload, other)
	}
	return payload
}

// quiet asserts no socket (authenticated or not) received the event.
func (a *feedAudience) quiet(t *testing.T, name string) {
	t.Helper()
	for _, socket := range []*harness.Socket{a.admin, a.listener, a.anon} {
		socket.None(t, a.marks[socket], name, quietWindow, nil)
	}
}

// silent asserts only the unauthenticated socket received nothing.
func (a *feedAudience) silent(t *testing.T, name string) {
	t.Helper()
	a.anon.None(t, a.marks[a.anon], name, quietWindow, nil)
}

// eventIDs lists the ids of the named events received since the mark.
func (a *feedAudience) eventIDs(t *testing.T, socket *harness.Socket, name string) []string {
	t.Helper()
	var ids []string
	for _, event := range socket.Events()[a.marks[socket]:] {
		if event.Name == name {
			ids = append(ids, eventPayload(t, event.Args).ID)
		}
	}
	return ids
}

// socketOpenScenarios covers socket-outgoing-rss-feed-open.
func socketOpenScenarios() []harness.Scenario {
	const req = "socket-outgoing-rss-feed-open"
	const inv = "INV-SOCKET-OUTGOING-RSS-FEED-OPEN"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "initial", Assertion: "rss-feed-open-reaches-every-authenticated-socket",
			Sources: []string{"server/managers/RssFeedManager.js:293-305", "server/SocketAuthority.js:57-65"},
			Claim:   "Opening an item feed emits one rss_feed_open to the root socket and to the non-admin listener socket with the same minified feed payload the open route returned, and nothing to an unauthenticated socket.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				audience := listenFeedEvents(t, e)
				feed := mustOpenFeed(t, e, "item", fx.novel, "feeds-evt-open", map[string]any{"ownerName": "Event Owner"})
				payload := audience.expect(t, "rss_feed_open", feed.ID)
				if !reflect.DeepEqual(payload, feed) {
					t.Fatalf("rss_feed_open payload %+v differs from the open response %+v", payload, feed)
				}
				if payload.EntityType != "libraryItem" || payload.EntityID != fx.novel || payload.FeedURL != "/feed/feeds-evt-open" {
					t.Fatalf("rss_feed_open identity = %+v", payload)
				}
				if payload.Meta.Title == nil || *payload.Meta.Title != "Single File Novel" || payload.Meta.PreventIndexing == nil || !*payload.Meta.PreventIndexing {
					t.Fatalf("rss_feed_open meta = %+v", payload.Meta)
				}
				if payload.Meta.OwnerName == nil || *payload.Meta.OwnerName != "Event Owner" || payload.Meta.OwnerEmail != nil {
					t.Fatalf("rss_feed_open owner = %+v", payload.Meta)
				}
				audience.silent(t, "rss_feed_open")
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "error", State: "initial", Assertion: "rss-feed-open-not-emitted-for-rejected-open",
			Sources: []string{"server/controllers/RSSFeedController.js:41-56,194-204", "server/managers/RssFeedManager.js:293-305"},
			Claim:   "An open request rejected by the admin middleware (non-admin 403) or by the auth layer (no token 401) emits no rss_feed_open to any socket.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				audience := listenFeedEvents(t, e)
				audience.reset()
				wantStatus(t, e.Post(t, e.Token(t, "listener"), openPath("item", fx.novel), openBody("feeds-evt-denied", nil)), http.StatusForbidden, "non-admin open")
				wantStatus(t, e.Post(t, "", openPath("item", fx.novel), openBody("feeds-evt-anon", nil)), http.StatusUnauthorized, "anonymous open")
				audience.quiet(t, "rss_feed_open")
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "edge", State: "initial", Assertion: "rss-feed-open-carries-collection-entity",
			Sources: []string{"server/managers/RssFeedManager.js:314-326"},
			Claim:   "Opening a collection feed emits rss_feed_open with entityType collection, the collection id and the collection's title to both authenticated sockets.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				audience := listenFeedEvents(t, e)
				audience.reset()
				feed := mustOpenFeed(t, e, "collection", fx.collection, "feeds-evt-coll", nil)
				payload := audience.expect(t, "rss_feed_open", feed.ID)
				if payload.EntityType != "collection" || payload.EntityID != fx.collection || payload.Meta.Title == nil || *payload.Meta.Title != "Favourites" {
					t.Fatalf("collection rss_feed_open payload = %+v", payload)
				}
				audience.silent(t, "rss_feed_open")
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "edge", State: "repeat", Assertion: "rss-feed-open-emits-once-per-open-in-order",
			Sources: []string{"server/managers/RssFeedManager.js:293-347"},
			Claim:   "Two opens emit two rss_feed_open events, one per open, in creation order on every authenticated socket.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				audience := listenFeedEvents(t, e)
				audience.reset()
				first := mustOpenFeed(t, e, "series", fx.series, "feeds-evt-ser", nil)
				second := mustOpenFeed(t, e, "item", fx.saga1, "feeds-evt-item", nil)
				audience.expect(t, "rss_feed_open", first.ID)
				audience.expect(t, "rss_feed_open", second.ID)
				for _, socket := range []*harness.Socket{audience.admin, audience.listener} {
					if ids := audience.eventIDs(t, socket, "rss_feed_open"); !reflect.DeepEqual(ids, []string{first.ID, second.ID}) {
						t.Fatalf("rss_feed_open order = %v, want [%s %s]", ids, first.ID, second.ID)
					}
				}
				audience.silent(t, "rss_feed_open")
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "edge", State: "changed", Assertion: "rss-feed-open-after-close-carries-new-id",
			Sources: []string{"server/managers/RssFeedManager.js:293-305,355-361"},
			Claim:   "After a feed is closed and the same entity is reopened with the same slug, rss_feed_open is emitted again with a new feed id and the same feedUrl.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				audience := listenFeedEvents(t, e)
				first := mustOpenFeed(t, e, "item", fx.novel, "feeds-evt-reopen", nil)
				audience.expect(t, "rss_feed_open", first.ID)
				wantStatus(t, closeFeed(t, e, e.Token(t, "root"), first.ID), http.StatusOK, "close before reopen")
				audience.reset()
				second := mustOpenFeed(t, e, "item", fx.novel, "feeds-evt-reopen", nil)
				if second.ID == first.ID {
					t.Fatalf("reopened feed reused id %s", first.ID)
				}
				payload := audience.expect(t, "rss_feed_open", second.ID)
				if payload.FeedURL != first.FeedURL || payload.EntityID != first.EntityID {
					t.Fatalf("reopened payload = %+v, want the same feedUrl/entity as %+v", payload, first)
				}
				audience.silent(t, "rss_feed_open")
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "error", State: "failure", Assertion: "rss-feed-open-not-emitted-for-failed-write",
			Sources: []string{"server/managers/RssFeedManager.js:293-305", "server/models/Feed.js:165-189"},
			Claim:   "Opens rejected for a used slug, an unknown item or an item without audio tracks emit no rss_feed_open to any socket and persist nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				audience := listenFeedEvents(t, e)
				audience.reset()
				before := snapshotCounts(t, e)
				wantStatus(t, openFeed(t, e, "item", fx.novel, "feeds-evt-open", nil), http.StatusBadRequest, "duplicate slug")
				wantStatus(t, openFeed(t, e, "item", "00000000-0000-4000-8000-000000000000", "feeds-evt-missing", nil), http.StatusNotFound, "unknown item")
				wantStatus(t, openFeed(t, e, "item", fx.ebook, "feeds-evt-noaudio", nil), http.StatusBadRequest, "no audio tracks")
				audience.quiet(t, "rss_feed_open")
				wantSameCounts(t, e, before, "failed opens")
			},
		},
	}
}

// socketClosedScenarios covers socket-outgoing-rss-feed-closed.
func socketClosedScenarios() []harness.Scenario {
	const req = "socket-outgoing-rss-feed-closed"
	const inv = "INV-SOCKET-OUTGOING-RSS-FEED-CLOSED"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "initial", Assertion: "rss-feed-closed-reaches-every-authenticated-socket",
			Sources: []string{"server/managers/RssFeedManager.js:355-361", "server/SocketAuthority.js:57-65"},
			Claim:   "Closing a feed through the management route emits one rss_feed_closed to the root and listener sockets with the minified payload of the closed feed, and nothing to an unauthenticated socket.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				audience := listenFeedEvents(t, e)
				feed := mustOpenFeed(t, e, "item", fx.saga1, "feeds-evt-close", nil)
				wantStatus(t, closeFeed(t, e, e.Token(t, "root"), feed.ID), http.StatusOK, "close feed")
				payload := audience.expect(t, "rss_feed_closed", feed.ID)
				if !reflect.DeepEqual(payload, feed) {
					t.Fatalf("rss_feed_closed payload %+v differs from the opened feed %+v", payload, feed)
				}
				if payload.Meta.Title == nil || feed.Meta.Title == nil || *payload.Meta.Title != *feed.Meta.Title {
					t.Fatalf("rss_feed_closed title = %v, want %v", payload.Meta.Title, feed.Meta.Title)
				}
				audience.silent(t, "rss_feed_closed")
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "error", State: "initial", Assertion: "rss-feed-closed-not-emitted-for-denied-close",
			Sources: []string{"server/controllers/RSSFeedController.js:176-204"},
			Claim:   "A close by a non-admin (403) or without a token (401) emits no rss_feed_closed to any socket and leaves the feed readable.",
			Run: func(t *testing.T, e *harness.Env) {
				audience := listenFeedEvents(t, e)
				audience.reset()
				wantStatus(t, closeFeed(t, e, e.Token(t, "listener"), saga1FeedID), http.StatusForbidden, "non-admin close")
				wantStatus(t, closeFeed(t, e, "", saga1FeedID), http.StatusUnauthorized, "anonymous close")
				audience.quiet(t, "rss_feed_closed")
				wantStatus(t, feedBody(t, e, saga1FeedSlug), http.StatusOK, "feed after denied close")
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "edge", State: "initial", Assertion: "rss-feed-closed-on-collection-delete",
			Sources: []string{"server/controllers/CollectionController.js:214-224", "server/managers/RssFeedManager.js:355-378"},
			Claim:   "Deleting a collection that has an open feed emits rss_feed_closed for that feed to every authenticated socket with the same minified feed the list returned, and removes the feed and its episodes.",
			Run: func(t *testing.T, e *harness.Env) {
				row := feedRowBySlug(t, e, mixedSlug)
				feedID, _ := row["id"].(string)
				collectionID, _ := row["entityId"].(string)
				expected := minifiedByID(t, listFeeds(t, e, e.Token(t, "root")), feedID)
				audience := listenFeedEvents(t, e)
				audience.reset()
				wantStatus(t, e.Delete(t, e.Token(t, "root"), "/api/collections/"+collectionID), http.StatusOK, "delete collection")
				payload := audience.expect(t, "rss_feed_closed", feedID)
				if !reflect.DeepEqual(payload, expected) {
					t.Fatalf("rss_feed_closed payload %+v differs from the listed feed %+v", payload, expected)
				}
				if payload.EntityType != "collection" || payload.EntityID != collectionID {
					t.Fatalf("collection delete event identity = %+v", payload)
				}
				noFeedRow(t, e, mixedSlug, feedID)
				if rows := feedEpisodeRows(t, e, feedID); len(rows) != 0 {
					t.Fatalf("collection delete left feed episodes: %v", rows)
				}
				audience.silent(t, "rss_feed_closed")
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "edge", State: "repeat", Assertion: "rss-feed-closed-emits-once",
			Sources: []string{"server/managers/RssFeedManager.js:355-361", "server/controllers/RSSFeedController.js:176-183"},
			Claim:   "Closing the same feed twice emits exactly one rss_feed_closed: the second close answers 404 and no socket receives a second event.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				audience := listenFeedEvents(t, e)
				feed := mustOpenFeed(t, e, "item", fx.novel, "feeds-evt-close2", nil)
				wantStatus(t, closeFeed(t, e, e.Token(t, "root"), feed.ID), http.StatusOK, "first close")
				audience.expect(t, "rss_feed_closed", feed.ID)
				audience.reset()
				wantStatus(t, closeFeed(t, e, e.Token(t, "root"), feed.ID), http.StatusNotFound, "second close")
				audience.quiet(t, "rss_feed_closed")
				for _, socket := range []*harness.Socket{audience.admin, audience.listener} {
					if ids := audience.eventIDs(t, socket, "rss_feed_closed"); len(ids) != 0 {
						t.Fatalf("second close emitted %v", ids)
					}
				}
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "edge", State: "changed", Assertion: "rss-feed-closed-on-item-delete",
			Sources: []string{"server/routers/ApiRouter.js:385-388", "server/managers/RssFeedManager.js:355-378"},
			Claim:   "Hard-deleting a library item that has an open feed emits rss_feed_closed for that feed to every authenticated socket and removes the feed with the item.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				feed := mustOpenFeed(t, e, "item", fx.disposable, "feeds-evt-delete", nil)
				expected := minifiedByID(t, listFeeds(t, e, e.Token(t, "root")), feed.ID)
				audience := listenFeedEvents(t, e)
				audience.reset()
				wantStatus(t, e.Do(t, harness.Request{Method: http.MethodDelete, Path: "/api/items/" + fx.disposable, Query: map[string][]string{"hard": {"1"}}, Token: e.Token(t, "root")}), http.StatusOK, "hard delete item")
				payload := audience.expect(t, "rss_feed_closed", feed.ID)
				if !reflect.DeepEqual(payload, expected) {
					t.Fatalf("item delete payload %+v differs from %+v", payload, expected)
				}
				noFeedRow(t, e, "feeds-evt-delete", feed.ID)
				if count := e.Count(t, "SELECT COUNT(*) FROM libraryItems WHERE id = ?", fx.disposable); count != 0 {
					t.Fatalf("deleted item still has %d rows", count)
				}
				audience.silent(t, "rss_feed_closed")
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "error", State: "failure", Assertion: "rss-feed-closed-not-emitted-for-unknown-feed",
			Sources: []string{"server/controllers/RSSFeedController.js:176-183", "server/managers/RssFeedManager.js:355-361"},
			Claim:   "Closing an unknown feed id answers 404, emits no rss_feed_closed and leaves every feed and episode row untouched.",
			Run: func(t *testing.T, e *harness.Env) {
				audience := listenFeedEvents(t, e)
				audience.reset()
				before := snapshotCounts(t, e)
				wantStatus(t, closeFeed(t, e, e.Token(t, "root"), "00000000-0000-4000-8000-000000000000"), http.StatusNotFound, "unknown close")
				audience.quiet(t, "rss_feed_closed")
				wantSameCounts(t, e, before, "unknown close")
			},
		},
	}
}
