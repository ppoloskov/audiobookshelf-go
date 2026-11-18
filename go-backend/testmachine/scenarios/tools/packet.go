// Package tools characterizes metadata-embed queue admission and dequeue events.
package tools

import (
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// Packet returns the metadata-embed queue and task-list scenarios.
func Packet() harness.Packet {
	return harness.Packet{
		Name: "tools",
		// The scenarios create several account and socket audiences; the packet
		// exists to characterize the embed queue, not the login rate limiter.
		Options: harness.Options{Env: map[string]string{"RATE_LIMIT_AUTH_MAX": "0"}},
		Scenarios: []harness.Scenario{
			{
				Requirement: "route-r204", Invariant: "INV-ROUTE-R204-EMPTY-001", Case: "edge", State: "stateless", Assertion: "idle-task-and-queue-response-shapes",
				Sources: []string{"server/routers/ApiRouter.js:347", "server/controllers/MiscController.js:116-129", "server/managers/AudioMetadataManager.js:29-31"},
				Claim:   "An idle GET /api/tasks returns an empty tasks array and no queuedTaskData key, include=queue adds a real empty embedMetadata array, and an unknown include value is ignored.",
				Run:     checkTasksQueryShapes,
			},
			{
				Requirement: "route-r204", Invariant: "INV-ROUTE-R204-AUTH-001", Case: "error", State: "stateless", Assertion: "task-list-requires-authentication-not-admin",
				Sources: []string{"server/Server.js:354", "server/routers/ApiRouter.js:347", "server/controllers/MiscController.js:116-129", "client/layouts/default.vue:578-590"},
				Claim:   "An anonymous GET /api/tasks is rejected with 401 while an ordinary authenticated user, for whom the layout loads tasks, receives the same 200 body as an admin without changing any task.",
				Run:     checkTasksAuthorisation,
			},
			{
				Requirement: "route-r204", Invariant: "INV-ROUTE-R204-ROUTE", Case: "normal", State: "stateless", Assertion: "running-task-and-fifo-queue-projection",
				Sources: []string{"server/controllers/MiscController.js:116-129", "server/objects/Task.js:54-74", "server/managers/AudioMetadataManager.js:80-104", "client/components/cards/ItemTaskRunningCard.vue:44-70", "client/layouts/default.vue:578-590"},
				Claim:   "While a forced four-item batch runs, GET /api/tasks?include=queue returns exactly one running embed task with its full client-rendered projection and the remaining three task documents in submission order, each with the forced option and the item's cache and media paths.",
				Run:     func(t *testing.T, e *harness.Env) { checkTaskProjection(t, e, "projection") },
			},
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
				Requirement: "socket-outgoing-metadata-embed-queue-update", Invariant: "INV-SOCKET-OUTGOING-METADATA-EMBED-QUEUE-UPDATE", Case: "normal", State: "initial", Assertion: "single-waiter-audience-and-order",
				Sources: []string{"server/managers/AudioMetadataManager.js:117-126", "server/managers/AudioMetadataManager.js:270-287", "server/managers/TaskManager.js:22-35", "server/SocketAuthority.js:57-88"},
				Claim:   "With one item running and one waiting, admins alone receive its single queued:true admission, every authenticated socket including a revoked-library user receives its single queued:false after the predecessor's task_finished and before its own task_started, and anonymous sockets receive neither.",
				Run:     func(t *testing.T, e *harness.Env) { checkSingleWaiter(t, e, "single-waiter") },
			},
			{
				Requirement: "socket-outgoing-metadata-embed-queue-update", Invariant: "INV-SOCKET-OUTGOING-METADATA-EMBED-QUEUE-UPDATE", Case: "normal", State: "changed", Assertion: "waiting-embed-admission-and-fifo-dequeue-audiences",
				Sources: []string{"server/controllers/ToolsController.js:113-154", "server/managers/AudioMetadataManager.js:51-55", "server/managers/AudioMetadataManager.js:63-133", "server/managers/AudioMetadataManager.js:264-286", "server/SocketAuthority.js:57-87"},
				Claim:   "A synchronous three-item batch starts the first immediately, emits exactly two queued:true admissions to admins, then emits queued:false for each waiter to all authenticated sockets in FIFO order after its predecessor finishes and before its own task starts; anonymous sockets receive neither transition.",
				Run:     func(t *testing.T, e *harness.Env) { checkBatch(t, e, "batch", []string{"saga1", "novel", "saga2"}) },
			},
			{
				Requirement: "cap-flow-tools-job-lifecycle", Invariant: "INV-CAP-FLOW-TOOLS-JOB-LIFECYCLE", Case: "normal", State: "initial", Assertion: "fifo-order-one-at-a-time",
				Sources: []string{"server/managers/AudioMetadataManager.js:20-35", "server/managers/AudioMetadataManager.js:51-56", "server/managers/AudioMetadataManager.js:117-135", "server/managers/AudioMetadataManager.js:266-287"},
				Claim:   "A three-item batch runs one embed at a time in submission order: each waiting task starts only after its predecessor's task_finished and finishes before the next one starts.",
				Run: func(t *testing.T, e *harness.Env) {
					checkBatch(t, e, "lifecycle-fifo", []string{"saga1", "novel", "saga2"})
				},
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
			{
				Requirement: "socket-outgoing-metadata-embed-queue-update", Invariant: "INV-SOCKET-OUTGOING-METADATA-EMBED-QUEUE-UPDATE", Case: "edge", State: "repeat", Assertion: "three-waiter-fifo-drain-repeated",
				Sources: []string{"server/controllers/ToolsController.js:112-151", "server/managers/AudioMetadataManager.js:117-135", "server/managers/AudioMetadataManager.js:270-287", "server/SocketAuthority.js:57-88"},
				Claim:   "A four-item batch queues three waiters that admins see admitted in order and every authenticated socket sees dequeued one at a time in the same order, and repeating the identical batch produces the same transcript without stale or duplicated events.",
				Run:     func(t *testing.T, e *harness.Env) { checkDeepQueueRepeat(t, e, "deep-queue") },
			},
			{
				Requirement: "cap-flow-tools-job-lifecycle", Invariant: "INV-CAP-FLOW-TOOLS-JOB-LIFECYCLE", Case: "edge", State: "repeat", Assertion: "queue-depth-three-drains-and-repeats-in-order",
				Sources: []string{"server/managers/AudioMetadataManager.js:20-35", "server/managers/AudioMetadataManager.js:117-135", "server/managers/AudioMetadataManager.js:270-287", "server/managers/TaskManager.js:22-37"},
				Claim:   "A four-item submission drains a three-deep queue one task at a time in admission order, and submitting the identical batch again reproduces that order with no task left running or waiting.",
				Run:     func(t *testing.T, e *harness.Env) { checkDeepQueueRepeat(t, e, "lifecycle-deep") },
			},
			{
				Requirement: "cap-flow-tools-job-lifecycle", Invariant: "INV-CAP-FLOW-TOOLS-JOB-LIFECYCLE", Case: "normal", State: "changed", Assertion: "drained-queue-starts-next-admission-immediately",
				Sources: []string{"server/managers/AudioMetadataManager.js:29-35", "server/managers/AudioMetadataManager.js:266-287", "server/controllers/ToolsController.js:84-101"},
				Claim:   "After a batch finishes and the queue is empty, a single admission of the last item starts and finishes immediately without a queue transition or a leftover waiting entry.",
				Run:     func(t *testing.T, e *harness.Env) { checkDrainedQueue(t, e, "drained") },
			},
			{
				Requirement: "cap-event-metadata-embed-queue", Invariant: "INV-CAP-EVENT-METADATA-EMBED-QUEUE-REJECT-001", Case: "error", State: "changed", Assertion: "occupied-item-admissions-emit-no-transition",
				Sources: []string{"server/controllers/ToolsController.js:84-101", "server/controllers/ToolsController.js:112-151", "server/managers/AudioMetadataManager.js:33-35"},
				Claim:   "Re-admitting the running item, a waiting item or a one-item batch while the queue is occupied is refused with 400 and emits no metadata_embed_queue_update, leaving the FIFO queue and the running batch's transcript unchanged.",
				Run:     func(t *testing.T, e *harness.Env) { checkDuplicateRejection(t, e, "reject-event") },
			},
			{
				Requirement: "socket-outgoing-metadata-embed-queue-update", Invariant: "INV-SOCKET-OUTGOING-METADATA-EMBED-QUEUE-UPDATE-REJECT-001", Case: "error", State: "changed", Assertion: "rejected-admission-reaches-no-socket",
				Sources: []string{"server/controllers/ToolsController.js:84-101", "server/controllers/ToolsController.js:112-151", "server/SocketAuthority.js:57-88"},
				Claim:   "Each refused duplicate admission reaches no socket of any audience: admins receive no extra queued:true, authenticated sockets no extra queued:false and anonymous sockets nothing at all.",
				Run:     func(t *testing.T, e *harness.Env) { checkDuplicateRejection(t, e, "reject-socket") },
			},
			{
				Requirement: "cap-flow-tools-job-lifecycle", Invariant: "INV-CAP-FLOW-TOOLS-JOB-LIFECYCLE-REJECT-001", Case: "error", State: "changed", Assertion: "rejected-duplicate-keeps-task-order",
				Sources: []string{"server/controllers/ToolsController.js:84-101", "server/controllers/ToolsController.js:112-151", "server/managers/AudioMetadataManager.js:33-35"},
				Claim:   "A refused duplicate admission creates no task record and does not disturb the running batch: the admitted items still start and finish one at a time in their original order.",
				Run:     func(t *testing.T, e *harness.Env) { checkDuplicateRejection(t, e, "reject-lifecycle") },
			},
			{
				Requirement: "cap-event-metadata-embed-queue", Invariant: "INV-CAP-EVENT-METADATA-EMBED-QUEUE-OPTIONS-001", Case: "normal", State: "force", Assertion: "forced-admission-keeps-queue-transitions",
				Sources: []string{"server/controllers/ToolsController.js:95-98", "server/controllers/ToolsController.js:145-148", "server/managers/AudioMetadataManager.js:65-75", "server/managers/AudioMetadataManager.js:99-102", "server/managers/AudioMetadataManager.js:117-126"},
				Claim:   "forceEmbedChapters=1 never bypasses the single running slot: a forced waiter is admitted with queued:true and dequeued with queued:false exactly like a default job, and a forced admission on an idle queue emits no transition.",
				Run:     func(t *testing.T, e *harness.Env) { checkForcedOptions(t, e, "force-event") },
			},
			{
				Requirement: "socket-outgoing-metadata-embed-queue-update", Invariant: "INV-SOCKET-OUTGOING-METADATA-EMBED-QUEUE-UPDATE", Case: "normal", State: "force", Assertion: "forced-admission-same-audience-and-payload",
				Sources: []string{"server/controllers/ToolsController.js:145-148", "server/managers/AudioMetadataManager.js:99-102", "server/managers/AudioMetadataManager.js:117-126", "server/managers/AudioMetadataManager.js:277-280", "server/SocketAuthority.js:57-88"},
				Claim:   "A forced admission produces the same two-field queue events to the same audiences as a default admission: queued:true to admins for the waiter and queued:false to every authenticated socket before it starts.",
				Run:     func(t *testing.T, e *harness.Env) { checkForcedOptions(t, e, "force-socket") },
			},
			{
				Requirement: "cap-flow-tools-job-lifecycle", Invariant: "INV-CAP-FLOW-TOOLS-JOB-LIFECYCLE", Case: "normal", State: "force", Assertion: "forced-embed-runs-one-at-a-time",
				Sources: []string{"server/managers/AudioMetadataManager.js:20-35", "server/managers/AudioMetadataManager.js:117-126", "server/managers/AudioMetadataManager.js:270-287"},
				Claim:   "A forced embed of a multi-file book also runs alone: the forced waiter starts only after the forced running task finishes, and both tasks complete successfully.",
				Run:     func(t *testing.T, e *harness.Env) { checkForcedOptions(t, e, "force-lifecycle") },
			},
			{
				Requirement: "cap-event-metadata-embed-queue", Invariant: "INV-CAP-EVENT-METADATA-EMBED-QUEUE-FAILURE-001", Case: "error", State: "failure", Assertion: "failed-embed-advances-waiters",
				Sources: []string{"server/managers/AudioMetadataManager.js:139-151", "server/managers/AudioMetadataManager.js:266-287", "server/objects/Task.js:104-111", "server/managers/TaskManager.js:32-37"},
				Claim:   "A running embed that fails (unwritable item directory, unwritable audio file or an FFmpeg tag failure) reports a failed task without a queue transition of its own, still dequeues each waiting item with queued:false so both run in admission order, and the item embeds successfully once the cause is removed.",
				Run:     func(t *testing.T, e *harness.Env) { checkFailureAdvance(t, e, "failure-event") },
			},
			{
				Requirement: "socket-outgoing-metadata-embed-queue-update", Invariant: "INV-SOCKET-OUTGOING-METADATA-EMBED-QUEUE-UPDATE-FAILURE-001", Case: "error", State: "failure", Assertion: "failed-predecessor-dequeues-waiter",
				Sources: []string{"server/managers/AudioMetadataManager.js:139-151", "server/managers/AudioMetadataManager.js:270-281", "server/managers/TaskManager.js:22-37", "server/SocketAuthority.js:57-88"},
				Claim:   "When the running embed fails, admins receive no queue event for the failing item and every authenticated socket still receives the queued:false dequeue of the next waiter after the failure's task_finished and before that waiter's task_started.",
				Run:     func(t *testing.T, e *harness.Env) { checkFailureAdvance(t, e, "failure-socket") },
			},
			{
				Requirement: "cap-flow-tools-job-lifecycle", Invariant: "INV-CAP-FLOW-TOOLS-JOB-LIFECYCLE-FAILURE-001", Case: "error", State: "failure", Assertion: "failed-embed-does-not-block-following-entries",
				Sources: []string{"server/managers/AudioMetadataManager.js:139-151", "server/managers/AudioMetadataManager.js:266-287", "server/objects/Task.js:104-111"},
				Claim:   "One failed embed does not stop the queue: the following waiting entries are dequeued, started and finished in admission order, the failed item's media is unchanged, a tag failure leaves the item cache the source does not clean up, and a retry after the cause is removed succeeds.",
				Run:     func(t *testing.T, e *harness.Env) { checkFailureAdvance(t, e, "failure-lifecycle") },
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
