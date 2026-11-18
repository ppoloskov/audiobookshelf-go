package share

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	eventWait   = 5 * time.Second
	quietWindow = 300 * time.Millisecond
)

// matchShareID matches a share event whose payload id is id.
func matchShareID(id string) func([]json.RawMessage) bool {
	return func(args []json.RawMessage) bool {
		return len(args) == 1 && shareEventID(args[0]) == id
	}
}

// shareEventID returns the id of a raw share event payload.
func shareEventID(raw json.RawMessage) string {
	var value struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value.ID
}

// shareAudience is the admin socket plus the sockets that must never receive a
// share event, each with the mark taken when the audience was created.
type shareAudience struct {
	admin    []*harness.Socket
	listener *harness.Socket
	anon     *harness.Socket
	marks    map[*harness.Socket]int
}

// listenShares connects two admin sockets (fan-out), a non-admin socket and an
// unauthenticated socket, then marks them.
func listenShares(t *testing.T, e *harness.Env) *shareAudience {
	t.Helper()
	a := &shareAudience{marks: map[*harness.Socket]int{}}
	a.admin = []*harness.Socket{e.Socket(t, e.Token(t, "root")), e.Socket(t, e.Token(t, "root"))}
	a.listener = e.Socket(t, e.Token(t, "listener"))
	a.anon = e.Socket(t, "")
	a.reset()
	return a
}

// reset marks the current position of every socket.
func (a *shareAudience) reset() {
	for _, socket := range append(append([]*harness.Socket{}, a.admin...), a.listener, a.anon) {
		a.marks[socket] = socket.Mark()
	}
}

// expect waits for the event on every admin socket and asserts each carries the
// same payload, returned as a decoded share.
func (a *shareAudience) expect(t *testing.T, name, id string) mediaItemShare {
	t.Helper()
	var first mediaItemShare
	for index, socket := range a.admin {
		event := socket.Wait(t, a.marks[socket], name, eventWait, matchShareID(id))
		value := shareEvent(t, event.Args)
		if index == 0 {
			first = value
			continue
		}
		if !reflect.DeepEqual(value, first) {
			t.Fatalf("admin sockets received different %s payloads: %+v and %+v", name, first, value)
		}
	}
	return first
}

// silent asserts the non-audience sockets received no such event.
func (a *shareAudience) silent(t *testing.T, name string) {
	t.Helper()
	a.listener.None(t, a.marks[a.listener], name, quietWindow, nil)
	a.anon.None(t, a.marks[a.anon], name, quietWindow, nil)
}

// shareOpenScenarios covers socket-outgoing-share-open.
func shareOpenScenarios() []harness.Scenario {
	const req = "socket-outgoing-share-open"
	const inv = "INV-SOCKET-OUTGOING-SHARE-OPEN"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "initial", Assertion: "share-open-reaches-admin-sockets",
			Sources: []string{"server/managers/ShareManager.js:136-143", "server/SocketAuthority.js:81-90"},
			Claim:   "Creating a share emits share_open once to every admin socket with the same client payload the create route returned, and to no non-admin or unauthenticated socket.",
			Run: func(t *testing.T, e *harness.Env) {
				audience := listenShares(t, e)
				created := mustCreateShare(t, e, "share-slug-event-open", mediaIDForItem(t, e, fixtureOf(e).saga1), 0, true)
				payload := audience.expect(t, "share_open", created.ID)
				if !reflect.DeepEqual(payload, created) {
					t.Fatalf("share_open payload %+v differs from the create response %+v", payload, created)
				}
				if payload.Slug != created.Slug || payload.IsDownloadable == nil || !*payload.IsDownloadable || payload.ExpiresAt != nil {
					t.Fatalf("share_open payload fields wrong: %s", summary(payload))
				}
				audience.silent(t, "share_open")
				wantStatus(t, removeShare(t, e, created.ID), 204, "cleanup event share")
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "edge", State: "repeat", Assertion: "share-open-one-event-per-create",
			Sources: []string{"server/managers/ShareManager.js:136-143", "server/SocketAuthority.js:81-90"},
			Claim:   "Each create emits its own share_open with the created share's payload; two creates produce two events in creation order on every admin socket.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				audience := listenShares(t, e)
				first := mustCreateShare(t, e, "share-slug-event-open-a", mediaIDForItem(t, e, fx.saga1), 0, true)
				second := mustCreateShare(t, e, "share-slug-event-open-b", mediaIDForItem(t, e, fx.ebook), 0, false)
				gotFirst := audience.expect(t, "share_open", first.ID)
				gotSecond := audience.expect(t, "share_open", second.ID)
				if !reflect.DeepEqual(gotFirst, first) || !reflect.DeepEqual(gotSecond, second) || first.ID == second.ID {
					t.Fatalf("create events wrong: %+v and %+v", gotFirst, gotSecond)
				}
				for _, socket := range audience.admin {
					var ids []string
					for _, event := range socket.Events()[audience.marks[socket]:] {
						if event.Name == "share_open" {
							ids = append(ids, shareEvent(t, event.Args).ID)
						}
					}
					if len(ids) != 2 || ids[0] != first.ID || ids[1] != second.ID {
						t.Fatalf("admin socket share_open order = %v, want [%s %s]", ids, first.ID, second.ID)
					}
				}
				audience.silent(t, "share_open")
				wantStatus(t, removeShare(t, e, first.ID), 204, "cleanup first event share")
				wantStatus(t, removeShare(t, e, second.ID), 204, "cleanup second event share")
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "changed", Assertion: "share-open-after-recreate-has-new-id",
			Sources: []string{"server/managers/ShareManager.js:136-143", "server/controllers/ShareController.js:384-400"},
			Claim:   "Deleting a share and recreating the same slug emits a second share_open carrying the new share id and the same slug.",
			Run: func(t *testing.T, e *harness.Env) {
				mediaID := mediaIDForItem(t, e, fixtureOf(e).saga1)
				audience := listenShares(t, e)
				first := mustCreateShare(t, e, "share-slug-event-recreate", mediaID, 0, true)
				audience.expect(t, "share_open", first.ID)
				wantStatus(t, removeShare(t, e, first.ID), 204, "delete first share")
				second := mustCreateShare(t, e, first.Slug, mediaID, 0, true)
				if second.ID == first.ID {
					t.Fatal("recreated share must have a new id")
				}
				payload := audience.expect(t, "share_open", second.ID)
				if !reflect.DeepEqual(payload, second) || payload.Slug != first.Slug {
					t.Fatalf("recreated share_open payload %+v differs from %+v", payload, second)
				}
				audience.silent(t, "share_open")
				wantStatus(t, removeShare(t, e, second.ID), 204, "cleanup recreated share")
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "error", State: "failure", Assertion: "share-open-silent-on-rejected-create",
			Sources: []string{"server/controllers/ShareController.js:320-352", "server/SocketAuthority.js:81-90"},
			Claim:   "Rejected creates (non-admin, duplicate slug, duplicate item and invalid expiry) emit no share_open to any socket and persist no row.",
			Run: func(t *testing.T, e *harness.Env) {
				fx := fixtureOf(e)
				mediaID := mediaIDForItem(t, e, fx.saga1)
				audience := listenShares(t, e)
				rows := e.Count(t, "SELECT COUNT(*) FROM mediaItemShares")
				wantStatus(t, createShare(t, e, e.Token(t, "listener"), "share-slug-event-denied", mediaID, 0, true), 403, "non-admin create")
				wantStatus(t, createShare(t, e, e.Token(t, "root"), novelSlug, mediaID, 0, true), 409, "used slug")
				wantStatus(t, createShare(t, e, e.Token(t, "root"), "share-slug-event-dup", mediaIDForItem(t, e, fx.novel), 0, true), 409, "used media")
				wantStatus(t, createShare(t, e, e.Token(t, "root"), "share-slug-event-bad", mediaID, -2, true), 400, "invalid expiry")
				if got := e.Count(t, "SELECT COUNT(*) FROM mediaItemShares"); got != rows {
					t.Fatalf("rejected creates changed the row count: %d -> %d", rows, got)
				}
				audience.silent(t, "share_open")
			},
		},
	}
}

// shareClosedScenarios covers socket-outgoing-share-closed.
func shareClosedScenarios() []harness.Scenario {
	const req = "socket-outgoing-share-closed"
	const inv = "INV-SOCKET-OUTGOING-SHARE-CLOSED"
	return []harness.Scenario{
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "initial", Assertion: "share-closed-reaches-admin-sockets",
			Sources: []string{"server/managers/ShareManager.js:149-165", "server/SocketAuthority.js:81-90"},
			Claim:   "Deleting a share emits share_closed once to every admin socket with the closed share's client payload and to no non-admin or unauthenticated socket.",
			Run: func(t *testing.T, e *harness.Env) {
				created := mustCreateShare(t, e, "share-slug-event-close", mediaIDForItem(t, e, fixtureOf(e).saga1), 0, true)
				audience := listenShares(t, e)
				wantStatus(t, removeShare(t, e, created.ID), 204, "delete share")
				payload := audience.expect(t, "share_closed", created.ID)
				if !reflect.DeepEqual(payload, created) {
					t.Fatalf("share_closed payload %+v differs from the created share %+v", payload, created)
				}
				audience.silent(t, "share_closed")
				noShareRow(t, e, created.ID)
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "edge", State: "repeat", Assertion: "share-closed-once-on-expiry",
			Sources: []string{"server/managers/ShareManager.js:114-133", "server/managers/ShareManager.js:149-165"},
			Claim:   "A scheduled expiry emits share_closed exactly once with the expiring share's payload; later reads of the expired slug return 404 and emit nothing further.",
			Run: func(t *testing.T, e *harness.Env) {
				created := mustCreateShare(t, e, "share-slug-event-expiry", mediaIDForItem(t, e, fixtureOf(e).ebook), time.Now().Add(3000*time.Millisecond).UnixMilli(), true)
				audience := listenShares(t, e)
				payload := audience.expect(t, "share_closed", created.ID)
				if !reflect.DeepEqual(payload, created) || payload.ExpiresAt == nil {
					t.Fatalf("expiry share_closed payload %+v differs from %+v", payload, created)
				}
				harness.Eventually(t, 5*time.Second, "expired share row removal", func() bool {
					return len(shareRows(t, e, created.ID)) == 0
				})
				wantStatus(t, publicGet(t, e, created.Slug, nil, "", nil), 404, "expired slug")
				audience.listener.None(t, audience.marks[audience.listener], "share_closed", quietWindow, nil)
				audience.anon.None(t, audience.marks[audience.anon], "share_closed", quietWindow, nil)
				for _, socket := range audience.admin {
					if count := countEvents(socket, audience.marks[socket], "share_closed"); count != 1 {
						t.Fatalf("admin socket got %d share_closed events, want exactly 1", count)
					}
				}
				wantStatus(t, removeShare(t, e, "00000000-0000-4000-8000-000000000000"), 404, "delete unknown id")
				audience.silent(t, "share_closed")
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "normal", State: "changed", Assertion: "share-closed-per-deleted-share",
			Sources: []string{"server/managers/ShareManager.js:149-165", "server/controllers/ShareController.js:384-400"},
			Claim:   "Each deleted share emits its own share_closed, so deleting a share, recreating the slug and deleting it again produces two payloads with distinct ids.",
			Run: func(t *testing.T, e *harness.Env) {
				mediaID := mediaIDForItem(t, e, fixtureOf(e).saga1)
				audience := listenShares(t, e)
				first := mustCreateShare(t, e, "share-slug-close-twice", mediaID, 0, true)
				wantStatus(t, removeShare(t, e, first.ID), 204, "delete first")
				firstPayload := audience.expect(t, "share_closed", first.ID)
				second := mustCreateShare(t, e, first.Slug, mediaID, 0, true)
				wantStatus(t, removeShare(t, e, second.ID), 204, "delete second")
				secondPayload := audience.expect(t, "share_closed", second.ID)
				if firstPayload.ID == secondPayload.ID || !reflect.DeepEqual(firstPayload, first) || !reflect.DeepEqual(secondPayload, second) {
					t.Fatalf("closed payloads wrong: %+v and %+v", firstPayload, secondPayload)
				}
				audience.silent(t, "share_closed")
			},
		},
		{
			Requirement: req, Invariant: inv, Case: "error", State: "failure", Assertion: "share-closed-silent-on-refused-delete",
			Sources: []string{"server/controllers/ShareController.js:384-390", "server/SocketAuthority.js:81-90"},
			Claim:   "Refused deletes (non-admin and unknown id) emit no share_closed, keep the public share open, and only the admin delete then emits exactly one.",
			Run: func(t *testing.T, e *harness.Env) {
				created := mustCreateShare(t, e, "share-slug-close-denied", mediaIDForItem(t, e, fixtureOf(e).saga1), 0, true)
				audience := listenShares(t, e)
				wantStatus(t, e.Delete(t, e.Token(t, "listener"), "/api/share/mediaitem/"+created.ID), 403, "listener delete")
				wantStatus(t, removeShare(t, e, "00000000-0000-4000-8000-000000000000"), 404, "unknown delete")
				audience.silent(t, "share_closed")
				wantStatus(t, publicGet(t, e, created.Slug, nil, "", nil), 200, "share remains open")
				wantStatus(t, removeShare(t, e, created.ID), 204, "admin delete")
				payload := audience.expect(t, "share_closed", created.ID)
				if !reflect.DeepEqual(payload, created) {
					t.Fatalf("share_closed payload %+v differs from %+v", payload, created)
				}
				audience.silent(t, "share_closed")
			},
		},
	}
}

// countEvents counts the named events on a socket since a mark.
func countEvents(socket *harness.Socket, from int, name string) int {
	count := 0
	for _, event := range socket.Events()[from:] {
		if event.Name == name {
			count++
		}
	}
	return count
}
