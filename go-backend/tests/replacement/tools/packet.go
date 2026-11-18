// Package tools characterizes metadata-embed queue admission and dequeue events.
package tools

import (
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

// Packet returns the bounded metadata-embed queue scenarios.
func Packet() harness.Packet {
	return harness.Packet{Name: "tools", Scenarios: []harness.Scenario{
		{
			Requirement: "cap-event-metadata-embed-queue", Invariant: "INV-CAP-EVENT-METADATA-EMBED-QUEUE", Case: "normal", State: "initial", Assertion: "immediate-embed-has-no-queued-transition",
			Sources: []string{"server/controllers/ToolsController.js:84-101", "server/managers/AudioMetadataManager.js:63-133", "server/managers/AudioMetadataManager.js:264-286", "server/SocketAuthority.js:57-87"},
			Claim:   "An idle single-item metadata embed starts and finishes successfully without metadata_embed_queue_update on admin, ordinary, restricted, permission-revoked or anonymous sockets.",
			Run: func(t *testing.T, e *harness.Env) {
				a := connectAudiences(t, e, "immediate")
				defer a.close()
				id := e.IDs["saga1"]
				wantStatus(t, e.Post(t, e.Token(t, "root"), "/api/tools/item/"+id+"/embed-metadata", nil), 200)
				a.waitFinished(t, id)
				a.barrier(t)
				a.requireTrace(t, []string{id})
			},
		},
		{
			Requirement: "socket-outgoing-metadata-embed-queue-update", Invariant: "INV-SOCKET-OUTGOING-METADATA-EMBED-QUEUE-UPDATE", Case: "normal", State: "changed", Assertion: "waiting-embed-admission-and-fifo-dequeue-audiences",
			Sources: []string{"server/controllers/ToolsController.js:113-154", "server/managers/AudioMetadataManager.js:51-55", "server/managers/AudioMetadataManager.js:63-133", "server/managers/AudioMetadataManager.js:264-286", "server/SocketAuthority.js:57-87"},
			Claim:   "A synchronous three-item batch starts the first immediately, emits exactly two queued:true admissions to admins, then emits queued:false for each waiter to all authenticated sockets in FIFO order after its predecessor finishes and before its own task starts; anonymous sockets receive neither transition.",
			Run:     func(t *testing.T, e *harness.Env) { checkBatch(t, e, "batch", []string{"saga1", "novel", "saga2"}) },
		},
		{
			Requirement: "cap-event-metadata-embed-queue", Invariant: "INV-CAP-EVENT-METADATA-EMBED-QUEUE", Case: "edge", State: "repeat", Assertion: "idle-queue-repeat-keeps-immediate-and-waiting-distinct",
			Sources: []string{"server/controllers/ToolsController.js:113-154", "server/managers/AudioMetadataManager.js:51-55", "server/managers/AudioMetadataManager.js:63-133", "server/managers/AudioMetadataManager.js:264-286", "server/SocketAuthority.js:57-87"},
			Claim:   "Two consecutive batches of the same items, with a different second order, each finish successfully, start only their first item immediately and emit exact FIFO waiting/dequeue transitions to the defined audiences without stale item IDs or duplicate events.",
			Run: func(t *testing.T, e *harness.Env) {
				checkBatch(t, e, "repeat-first", []string{"saga1", "novel", "saga2"})
				checkBatch(t, e, "repeat-second", []string{"novel", "saga2", "saga1"})
			},
		},
	}}
}

func checkBatch(t *testing.T, e *harness.Env, suffix string, keys []string) {
	t.Helper()
	a := connectAudiences(t, e, suffix)
	defer a.close()
	ids := make([]string, len(keys))
	for i, key := range keys {
		ids[i] = e.IDs[key]
	}
	wantStatus(t, e.Post(t, e.Token(t, "root"), "/api/tools/batch/embed-metadata", struct {
		LibraryItemIDs []string `json:"libraryItemIds"`
	}{ids}), 200)
	// handleBatchEmbed invokes admission synchronously. The first job enters
	// tasksRunning before its first await, so both later jobs must wait even
	// when the native FFmpeg operations are very fast.
	a.waitFinished(t, ids[len(ids)-1])
	a.barrier(t)
	a.requireTrace(t, ids)
}
