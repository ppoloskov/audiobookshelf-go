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
		Scenarios: append([]harness.Scenario{
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
		}, sliceB()...),
	}
}

// b builds one slice-B scenario declaration.
func b(requirement, invariant, c, state, assertion string, sources []string, claim string, run func(*testing.T, *harness.Env)) harness.Scenario {
	return harness.Scenario{
		Requirement: requirement, Invariant: invariant, Case: c, State: state, Assertion: assertion,
		Sources: sources, Claim: claim, Run: run,
	}
}

// sliceB declares the audio task lifecycle, progress events and the four tools
// routes. It runs after the queue declarations and consumes the fixture items
// progressively: embed-only checks first, batch checks next, then the encode
// route whose success runs replace audio files, and the failure checks last.
func sliceB() []harness.Scenario {
	const (
		taskLifecycle = "cap-event-task-lifecycle"
		taskStarted   = "socket-outgoing-task-started"
		taskFinished  = "socket-outgoing-task-finished"
		trackStarted  = "socket-outgoing-track-started"
		trackProgress = "socket-outgoing-track-progress"
		taskProgress  = "socket-outgoing-task-progress"
		trackFinished = "socket-outgoing-track-finished"
		capTrackFin   = "cap-event-track-finished"
		capTrackProg  = "cap-event-track-progress"
		r183          = "route-r183"
		r184          = "route-r184"
		r185          = "route-r185"
		r186          = "route-r186"
		invTask       = "INV-CAP-EVENT-TASK-LIFECYCLE"
		invTaskStart  = "INV-SOCKET-OUTGOING-TASK-STARTED"
		invTaskFinish = "INV-SOCKET-OUTGOING-TASK-FINISHED"
		invTrackStart = "INV-SOCKET-OUTGOING-TRACK-STARTED"
		invTrackProg  = "INV-SOCKET-OUTGOING-TRACK-PROGRESS"
		invTrackFin   = "INV-SOCKET-OUTGOING-TRACK-FINISHED"
		invCapTrackF  = "INV-CAP-EVENT-TRACK-FINISHED"
		invCapTrackP  = "INV-CAP-EVENT-TRACK-PROGRESS"
	)
	var (
		taskJSONSources = []string{"server/managers/TaskManager.js:22-37", "server/objects/Task.js:54-97", "server/SocketAuthority.js:57-65"}
		embedSources    = []string{"server/controllers/ToolsController.js:84-101", "server/managers/AudioMetadataManager.js:64-136", "server/managers/AudioMetadataManager.js:266-287"}
		encodeSources   = []string{"server/controllers/ToolsController.js:29-55", "server/managers/AbMergeManager.js:57-102", "server/managers/AbMergeManager.js:104-252"}
		trackAMMSources = []string{"server/managers/AudioMetadataManager.js:202-253", "server/SocketAuthority.js:80-87", "client/layouts/default.vue:242-251"}
		trackABSources  = []string{"server/managers/AbMergeManager.js:142-155", "server/objects/TrackProgressMonitor.js:27-87", "server/SocketAuthority.js:80-87"}
		progressSources = []string{"server/managers/AudioMetadataManager.js:230-234", "server/managers/AbMergeManager.js:146-153", "server/managers/AbMergeManager.js:180-183", "server/utils/ffmpegHelpers.js:302-394", "client/layouts/default.vue:253-261"}
		batchSources    = []string{"server/controllers/ToolsController.js:112-151", "server/managers/AudioMetadataManager.js:52-56", "server/managers/AudioMetadataManager.js:117-127", "client/components/app/Appbar.vue:201-224"}
		cancelSources   = []string{"server/controllers/ToolsController.js:66-73", "server/managers/AbMergeManager.js:42-49", "server/managers/AbMergeManager.js:254-299", "server/utils/ffmpegHelpers.js:447-522"}
	)
	return []harness.Scenario{
		// Task lifecycle documents and audiences on a default embed.
		b(taskLifecycle, invTask, "normal", "initial", "embed-task-lifecycle-json-and-audience", taskJSONSources,
			"An idle embed reports task_started and task_finished with the full client-rendered Task projection to every authenticated socket, rewrites the audio file and reaches no anonymous socket.",
			func(t *testing.T, e *harness.Env) { checkEmbedTaskLifecycle(t, e, "lifecycle-json") }),
		b(taskStarted, invTaskStart, "normal", "initial", "task-started-audience-and-json", taskJSONSources,
			"The task_started of an idle embed carries the whole task document to every authenticated socket and none to anonymous sockets.",
			func(t *testing.T, e *harness.Env) { checkEmbedTaskLifecycle(t, e, "started-json") }),
		b(taskFinished, invTaskFinish, "normal", "initial", "task-finished-audience-and-json", taskJSONSources,
			"The task_finished of a successful embed closes the started task with terminal flags and timestamps on every authenticated socket and none on anonymous sockets.",
			func(t *testing.T, e *harness.Env) { checkEmbedTaskLifecycle(t, e, "finished-json") }),

		// Per-file track events of a three-file embed.
		b(trackStarted, invTrackStart, "edge", "changed", "embed-track-started-per-file-order", trackAMMSources,
			"A three-file embed emits one admin-only track_started per audio file in task order, each before that file is tagged.",
			func(t *testing.T, e *harness.Env) { checkEmbedTrackSequence(t, e, "track-started-order") }),
		b(capTrackFin, invCapTrackF, "normal", "initial", "embed-track-finished-payload", trackAMMSources,
			"Every successfully tagged audio file of an embed emits the admin-only track_finished with the item id and the file's ino.",
			func(t *testing.T, e *harness.Env) { checkEmbedTrackSequence(t, e, "cap-track-finished") }),
		b(trackFinished, invTrackFin, "normal", "initial", "embed-track-finished-admin-only", trackAMMSources,
			"An embed emits one admin-only track_finished per tagged file and none to non-admin or anonymous sockets.",
			func(t *testing.T, e *harness.Env) { checkEmbedTrackSequence(t, e, "track-finished-audience") }),
		b(trackStarted, invTrackStart, "normal", "repeat", "embed-track-started-repeats-per-job", trackAMMSources,
			"A second embed of the same book repeats the same per-file track_started sequence with the file inos of the new job.",
			func(t *testing.T, e *harness.Env) { checkEmbedTrackSequence(t, e, "track-started-repeat") }),
		b(trackFinished, invTrackFin, "edge", "changed", "three-file-track-finished-order", trackAMMSources,
			"The track_finished events of a three-file embed arrive one file at a time, each after its progress pair and before the next file's track_started.",
			func(t *testing.T, e *harness.Env) { checkEmbedTrackSequence(t, e, "track-finished-order") }),
		b(capTrackFin, invCapTrackF, "edge", "changed", "multi-file-track-finished-transition", trackAMMSources,
			"A three-file embed transitions track by track: each file's track_finished precedes the next file's track_started and the job's task_finished.",
			func(t *testing.T, e *harness.Env) { checkEmbedTrackSequence(t, e, "cap-track-transition") }),
		b(trackFinished, invTrackFin, "normal", "repeat", "track-finished-repeats-per-file", trackAMMSources,
			"A repeated embed emits the same one-track_finished-per-file transcript again without stale or duplicated events.",
			func(t *testing.T, e *harness.Env) { checkEmbedTrackSequence(t, e, "track-finished-repeat") }),
		b(capTrackFin, invCapTrackF, "normal", "repeat", "cap-track-finished-repeats-per-file", trackAMMSources,
			"A repeated embed of the same book repeats the admin-only track_finished transcript, one event per tagged file.",
			func(t *testing.T, e *harness.Env) { checkEmbedTrackSequence(t, e, "cap-track-finished-repeat") }),

		// Progress events of embeds and their duration weighting.
		b(trackProgress, invTrackProg, "normal", "initial", "embed-track-progress-admin-only", progressSources,
			"While each audio file of an embed is tagged, admin sockets receive track_progress with the file's ino and a percentage value, and non-admin sockets receive none.",
			func(t *testing.T, e *harness.Env) { checkEmbedProgress(t, e, "track-progress") }),
		b(capTrackProg, invCapTrackP, "normal", "initial", "embed-progress-payloads-and-weighting", progressSources,
			"An embed reports admin-only task_progress and track_progress per tagged file with the task_progress final value at the file's duration-weighted cumulative share.",
			func(t *testing.T, e *harness.Env) { checkEmbedProgress(t, e, "cap-progress") }),
		b(taskProgress, "INV-SOCKET-OUTGOING-TASK-PROGRESS", "edge", "changed", "embed-task-progress-duration-weighted", progressSources,
			"The task_progress of a three-file embed accumulates each file's duration share, ending near 100 when the last file is tagged.",
			func(t *testing.T, e *harness.Env) { checkEmbedProgress(t, e, "task-progress-weighted") }),
		b(trackProgress, invTrackProg, "normal", "repeat", "track-progress-values-per-track", progressSources,
			"A repeated embed produces the same per-file track_progress transcript with percentage values inside the observed ffmpeg range.",
			func(t *testing.T, e *harness.Env) { checkEmbedProgress(t, e, "track-progress-repeat") }),
		b(capTrackProg, invCapTrackP, "normal", "repeat", "progress-repeats-per-job", progressSources,
			"A repeated embed repeats the per-file progress transcript with task_progress before track_progress per file.",
			func(t *testing.T, e *harness.Env) { checkEmbedProgress(t, e, "progress-repeat") }),

		// Failed embed: failed task reflection and missing track events.
		b(taskLifecycle, invTask, "error", "failure", "failed-embed-task-reflection", append(taskJSONSources, "server/managers/AudioMetadataManager.js:139-151"),
			"An embed whose target directory is not writable reports task_failed through task_finished with the source error key on every authenticated socket while media and cache stay untouched.",
			func(t *testing.T, e *harness.Env) { checkEmbedTaskFailed(t, e, "task-failed-json") }),
		b(taskStarted, invTaskStart, "error", "failure", "failed-embed-still-starts-clean", taskJSONSources,
			"A failing embed still emits a clean task_started (no error fields, no terminal flags) before its failed task_finished.",
			func(t *testing.T, e *harness.Env) { checkEmbedTaskFailed(t, e, "failed-started") }),
		b(taskFinished, invTaskFinish, "error", "failure", "failed-embed-finishes-with-error-json", taskJSONSources,
			"The task_finished of a failing embed carries isFailed, the error text and the error key the client translates.",
			func(t *testing.T, e *harness.Env) { checkEmbedTaskFailed(t, e, "failed-finished") }),
		b(r185, "INV-ROUTE-R185-ROUTE", "error", "failure", "unwritable-target-fails-embed-unchanged", embedSources,
			"POST embed-metadata on a book whose directory is unwritable answers 200, fails the task with the target-directory error and leaves the audio file and the item cache untouched.",
			func(t *testing.T, e *harness.Env) { checkEmbedTaskFailed(t, e, "route-failure") }),

		// Repeat embeds: fresh task documents.
		b(taskLifecycle, invTask, "normal", "repeat", "repeat-embed-gets-fresh-task-document", taskJSONSources,
			"Two consecutive embeds of one book produce two distinct task documents, the second starting only after the first finished.",
			func(t *testing.T, e *harness.Env) { checkEmbedTaskRepeat(t, e, "task-repeat") }),
		b(taskStarted, invTaskStart, "normal", "repeat", "repeat-embed-starts-fresh-task", taskJSONSources,
			"The task_started of a repeated embed has a fresh id and a startedAt at or after the previous task's finishedAt.",
			func(t *testing.T, e *harness.Env) { checkEmbedTaskRepeat(t, e, "started-repeat") }),
		b(taskFinished, invTaskFinish, "edge", "repeat", "repeat-embed-finishes-fresh-task", taskJSONSources,
			"A repeated embed finishes its own fresh task document without reusing the previous task's identity.",
			func(t *testing.T, e *harness.Env) { checkEmbedTaskRepeat(t, e, "finished-repeat") }),
		b(r185, "INV-ROUTE-R185-REPEAT", "normal", "repeat", "embed-metadata-repeats-with-fresh-tasks", embedSources,
			"A repeated POST embed-metadata is accepted again and runs a fresh task document to success without stale state.",
			func(t *testing.T, e *harness.Env) { checkEmbedTaskRepeat(t, e, "route-repeat") }),

		// Forced embeds: recorded option and chapters.
		b(taskLifecycle, invTask, "edge", "force", "forced-embed-task-chapters-and-options", append(embedSources, "server/managers/AudioMetadataManager.js:74-75"),
			"forceEmbedChapters=1 records the option in the task data and embeds the book's chapters where a default multi-file embed carries none.",
			func(t *testing.T, e *harness.Env) { checkEmbedTaskForce(t, e, "task-force") }),
		b(taskStarted, invTaskStart, "edge", "force", "forced-embed-task-started-options", taskJSONSources,
			"The task_started of a forced embed carries forceEmbedChapters true and the book's chapters to the same audiences as a default job.",
			func(t *testing.T, e *harness.Env) { checkEmbedTaskForce(t, e, "started-force") }),
		b(taskFinished, invTaskFinish, "edge", "force", "forced-embed-task-finished-json", taskJSONSources,
			"A forced embed finishes with the same terminal projection as a default job.",
			func(t *testing.T, e *harness.Env) { checkEmbedTaskForce(t, e, "finished-force") }),
		b(r185, "INV-ROUTE-R185-ROUTE", "edge", "force", "forced-embed-options-and-chapters", embedSources,
			"The embed route accepts forceEmbedChapters=1, records it in the task data and embeds the chapters.",
			func(t *testing.T, e *harness.Env) { checkEmbedTaskForce(t, e, "route-force") }),

		// Encode route success and repeat.
		b(r183, "INV-ROUTE-R183-ROUTE", "normal", "initial", "encode-success-replaces-source", encodeSources,
			"POST encode-m4b on a book with audio answers 200, runs an encode-m4b task to success, replaces the source audio with the merged m4b beside it and keeps the original in the item cache.",
			func(t *testing.T, e *harness.Env) { checkEncodeSuccess(t, e, "encode-success") }),
		b(r183, "INV-ROUTE-R183-REPEAT", "normal", "repeat", "encode-repeats-after-completion", encodeSources,
			"A repeated POST encode-m4b after the first finished runs a fresh encode task again over the already replaced audio and succeeds.",
			func(t *testing.T, e *harness.Env) { checkEncodeSuccess(t, e, "encode-repeat") }),
		b(r183, "INV-ROUTE-R183-ROUTE", "error", "initial", "encode-route-authorization-and-validation", []string{"server/routers/ApiRouter.js:302", "server/controllers/ToolsController.js:29-55", "server/controllers/ToolsController.js:159-178", "server/models/LibraryItem.js:191-238"},
			"The encode route refuses anonymous callers with 401, non-admins with 403 before the item lookup, unknown ids with 404, books without audio with 400, and podcast items with 404, and no rejected request admits a task.",
			func(t *testing.T, e *harness.Env) { checkEncodeRouteAuthorization(t, e, "encode-auth") }),
		b(r183, "INV-ROUTE-R183-ROUTE", "edge", "changed", "pending-encode-rejections-and-concurrent-item", append(encodeSources, "server/controllers/ToolsController.js:45-48"),
			"While one encode is pending a repeated POST on the same item is refused with 400, unfit and unknown items are still refused, and a different item encodes concurrently.",
			func(t *testing.T, e *harness.Env) { checkEncodePending(t, e, "encode-pending") }),
		b(r183, "INV-ROUTE-R183-ROUTE", "error", "failure", "encode-merge-failure-fails-task", append(encodeSources, "server/managers/AbMergeManager.js:156-169", "server/utils/ffmpegHelpers.js:447-522"),
			"An encode whose input file is unreadable answers 200, emits the merge-phase track_started and a failed task_finished with the merge error, and leaves the media and cache metadata untouched.",
			func(t *testing.T, e *harness.Env) { checkEncodeFailure(t, e, "encode-failure") }),

		// Cancel route.
		b(r184, "INV-ROUTE-R184-ROUTE", "normal", "initial", "cancel-encode-stops-work-and-fails-task", cancelSources,
			"DELETE encode-m4b on a running encode answers 200, fails the task with 'Task canceled by user', leaves the media and item directory untouched and removes the cache's temp and metadata files.",
			func(t *testing.T, e *harness.Env) { checkEncodeCancel(t, e, "cancel-initial") }),
		b(r184, "INV-ROUTE-R184-REPEAT", "normal", "repeat", "repeat-cancel-leaves-no-pending-task", cancelSources,
			"After a cancel removed the task, a repeated DELETE finds no pending encode and answers 404.",
			func(t *testing.T, e *harness.Env) { checkEncodeCancel(t, e, "cancel-repeat") }),
		b(r184, "INV-ROUTE-R184-ROUTE", "error", "failure", "rejected-cancel-changes-nothing", cancelSources,
			"A DELETE with nothing to cancel answers 404 and leaves every task, file and event state unchanged.",
			func(t *testing.T, e *harness.Env) { checkEncodeCancel(t, e, "cancel-failure") }),
		b(r184, "INV-ROUTE-R184-ROUTE", "edge", "changed", "delete-after-finish-finds-nothing", []string{"server/controllers/ToolsController.js:66-73", "server/managers/AbMergeManager.js:32-34", "server/managers/AbMergeManager.js:249-251"},
			"After an encode finished and removed itself from the pending list, DELETE answers 404 for its item and for items that never had a task.",
			func(t *testing.T, e *harness.Env) { checkEncodeDeleteAfterFinish(t, e, "delete-after-finish") }),
		b(r184, "INV-ROUTE-R184-ROUTE", "error", "initial", "delete-route-authorization", []string{"server/routers/ApiRouter.js:303", "server/controllers/ToolsController.js:66-73", "server/controllers/ToolsController.js:159-178"},
			"The cancel route refuses anonymous callers with 401 and non-admins with 403 before the item lookup, and unknown ids with 404 for admins.",
			func(t *testing.T, e *harness.Env) { checkEncodeRouteAuthorization(t, e, "delete-auth") }),
		b(taskLifecycle, invTask, "normal", "changed", "canceled-encode-reflected-in-task-json", append(taskJSONSources, "server/managers/AbMergeManager.js:42-49"),
			"A canceled encode finishes with isFailed and the cancel message and key in the task document that every authenticated socket receives.",
			func(t *testing.T, e *harness.Env) { checkEncodeCancel(t, e, "canceled-json") }),
		b(taskFinished, invTaskFinish, "normal", "changed", "canceled-encode-finishes-with-cancel-error", taskJSONSources,
			"The task_finished of a canceled encode carries the cancel error, key and null substitutions to the authenticated audience.",
			func(t *testing.T, e *harness.Env) { checkEncodeCancel(t, e, "canceled-finished") }),

		// Batch route.
		b(r186, "INV-ROUTE-R186-ROUTE", "normal", "initial", "batch-embed-runs-all-members", batchSources,
			"POST batch embed-metadata admits every listed book, runs them one at a time to success and leaves no task or queue entry behind.",
			func(t *testing.T, e *harness.Env) { checkBatchFlow(t, e, "batch-flow") }),
		b(r186, "INV-ROUTE-R186-REPEAT", "normal", "repeat", "batch-repeats-with-fresh-tasks", batchSources,
			"The identical batch submitted again is accepted and runs fresh task documents for every member.",
			func(t *testing.T, e *harness.Env) { checkBatchFlow(t, e, "batch-repeat") }),
		b(r186, "INV-ROUTE-R186-ROUTE", "error", "initial", "batch-route-authorization-and-validation", append(batchSources, "server/controllers/ToolsController.js:159-178", "server/models/LibraryItem.js:191-238"),
			"The batch route refuses anonymous callers with 401, non-admins and restricted admins with 403, an empty body with 400 and the client-shown message, unknown and podcast ids with 404 and unfit books with 400, admitting nothing.",
			func(t *testing.T, e *harness.Env) { checkBatchValidation(t, e, "batch-auth") }),
		b(r186, "INV-ROUTE-R186-ROUTE", "error", "failure", "rejected-batch-admits-nothing", batchSources,
			"A batch whose member list fails validation admits no task for any member and leaves the queue and task state unchanged.",
			func(t *testing.T, e *harness.Env) { checkBatchValidation(t, e, "batch-failure") }),
		b(r186, "INV-ROUTE-R186-ROUTE", "edge", "initial", "batch-duplicate-ids-queue-once", batchSources,
			"A batch listing one item twice admits both entries: the first runs while the second waits in the queue, and both finish in submission order with distinct tasks.",
			func(t *testing.T, e *harness.Env) { checkBatchDuplicateIds(t, e, "batch-duplicates") }),
		b(r186, "INV-ROUTE-R186-ROUTE", "edge", "force", "batch-force-option-recorded", append(batchSources, "server/managers/AudioMetadataManager.js:74-75"),
			"A batch submitted with forceEmbedChapters=1 records the option and the book's chapters in the admitted task.",
			func(t *testing.T, e *harness.Env) { checkBatchForce(t, e, "batch-force") }),
		b(r186, "INV-ROUTE-R186-ROUTE", "error", "changed", "busy-member-rejects-batch", batchSources,
			"A batch containing an item that is currently processing is refused with 400 and the other member is not admitted.",
			func(t *testing.T, e *harness.Env) { checkBatchBusy(t, e, "batch-busy") }),

		// Embed route: default success, backup option and cache reuse.
		b(r185, "INV-ROUTE-R185-ROUTE", "normal", "initial", "embed-metadata-default-embed", append(embedSources, "client/pages/audiobook/_id/manage.vue:384-394"),
			"POST embed-metadata without options answers 200, runs the embed task to success and rewrites the audio files in place.",
			func(t *testing.T, e *harness.Env) { checkEmbedTaskLifecycle(t, e, "embed-default") }),
		b(r185, "INV-ROUTE-R185-ROUTE", "edge", "initial", "backup-option-keeps-cache-and-rewrites", append(embedSources, "server/managers/AudioMetadataManager.js:211-228", "server/managers/AudioMetadataManager.js:256-264"),
			"POST embed-metadata with backup=1 records backupFiles in the task, rewrites the audio file and keeps the item cache with the backup copy and the metadata file.",
			func(t *testing.T, e *harness.Env) { checkEmbedBackupOption(t, e, "embed-backup") }),
		b(r185, "INV-ROUTE-R185-ROUTE", "edge", "changed", "embed-cache-reuse-keeps-directory", append(embedSources, "server/managers/AudioMetadataManager.js:170-186", "server/managers/AudioMetadataManager.js:256-264"),
			"An embed whose item cache directory already exists keeps the directory and its earlier contents and removes only its own metadata file.",
			func(t *testing.T, e *harness.Env) { checkEmbedCacheReuse(t, e, "embed-reuse") }),

		// Encode job events (merge and metadata-embed phases).
		b(trackStarted, invTrackStart, "normal", "initial", "encode-track-started-admin-only", trackABSources,
			"An encode emits the admin-only track_started of the merged item's tracks from the progress monitor and none to non-admin sockets.",
			func(t *testing.T, e *harness.Env) { checkEncodeEvents(t, e, "encode-track-started") }),
		b(taskProgress, "INV-SOCKET-OUTGOING-TASK-PROGRESS", "normal", "initial", "encode-task-progress-phase-weighted", progressSources,
			"An encode reports admin-only task_progress scaled by the encode fraction during the merge and at the embed fraction afterwards.",
			func(t *testing.T, e *harness.Env) { checkEncodeEvents(t, e, "encode-task-progress") }),
		b(taskProgress, "INV-SOCKET-OUTGOING-TASK-PROGRESS", "normal", "repeat", "task-progress-repeats-per-encode", progressSources,
			"A repeated encode repeats the same task_progress composition of merge-phase and embed-phase events.",
			func(t *testing.T, e *harness.Env) { checkEncodeEvents(t, e, "encode-task-progress-repeat") }),
		b(trackProgress, invTrackProg, "edge", "changed", "encode-track-progress-before-task-progress", trackABSources,
			"During an encode's merge each track_progress event precedes its task_progress event and both carry percentage values.",
			func(t *testing.T, e *harness.Env) { checkEncodeEvents(t, e, "encode-track-progress") }),
		b(trackFinished, invTrackFin, "normal", "repeat", "encode-track-finished-single-track", trackABSources,
			"An encode finishes its item's tracks with exactly one admin-only track_finished after the merge progress events.",
			func(t *testing.T, e *harness.Env) { checkEncodeEvents(t, e, "encode-track-finished") }),
		b(capTrackProg, invCapTrackP, "edge", "changed", "encode-progress-phase-composition", progressSources,
			"An encode's task_progress stays below the 95 encode fraction during the merge and reaches at least 95 in the metadata-embed phase.",
			func(t *testing.T, e *harness.Env) { checkEncodeEvents(t, e, "encode-progress-phases") }),
		b(taskStarted, invTaskStart, "normal", "changed", "encode-task-started-payload-and-options", append(taskJSONSources, "server/managers/AbMergeManager.js:57-102"),
			"The task_started of an encode carries the encode-m4b projection with the item link, the recorded query options as strings and the file targets.",
			func(t *testing.T, e *harness.Env) { checkEncodeEvents(t, e, "encode-task-started") }),
		b(taskFinished, invTaskFinish, "edge", "changed", "encode-task-finished-success-json", append(taskJSONSources, "server/managers/AbMergeManager.js:249-251"),
			"A successful encode finishes with a clean terminal task document on the same authenticated audience as embeds.",
			func(t *testing.T, e *harness.Env) { checkEncodeEvents(t, e, "encode-task-finished") }),

		// Tag failure inside an embed: the failed file's events stop.
		b(trackFinished, invTrackFin, "error", "failure", "failed-track-gets-no-track-finished", trackAMMSources,
			"When tagging an audio file fails, that file receives no track_finished while the previously tagged file's cycle completed.",
			func(t *testing.T, e *harness.Env) { checkEmbedTagFailure(t, e, "tag-failure-finished") }),
		b(trackStarted, invTrackStart, "error", "failure", "failing-track-still-started", trackAMMSources,
			"The failing file of an embed still receives its track_started before the tag error fails the task.",
			func(t *testing.T, e *harness.Env) { checkEmbedTagFailure(t, e, "tag-failure-started") }),
		b(trackProgress, invTrackProg, "error", "failure", "failing-track-emits-no-progress", progressSources,
			"The failing file of an embed emits no track_progress and no further task_progress.",
			func(t *testing.T, e *harness.Env) { checkEmbedTagFailure(t, e, "tag-failure-progress") }),
		b(taskProgress, "INV-SOCKET-OUTGOING-TASK-PROGRESS", "error", "failure", "failed-job-emits-no-task-progress", progressSources,
			"After the failing file of an embed starts, no further task_progress is emitted before the failed task_finished.",
			func(t *testing.T, e *harness.Env) { checkEmbedTagFailure(t, e, "tag-failure-task-progress") }),
		b(capTrackFin, invCapTrackF, "error", "failure", "no-track-finished-for-failed-track", trackAMMSources,
			"A track that fails to embed reports no track_finished, only the task_failed through the task document.",
			func(t *testing.T, e *harness.Env) { checkEmbedTagFailure(t, e, "cap-tag-failure") }),
		b(capTrackProg, invCapTrackP, "error", "failure", "no-progress-after-failing-file", progressSources,
			"An embed that fails inside a file's tag run stops its progress events at that file.",
			func(t *testing.T, e *harness.Env) { checkEmbedTagFailure(t, e, "cap-tag-progress") }),

		// Rejected admissions reach no socket.
		b(taskLifecycle, invTask, "error", "changed", "rejected-admission-emits-no-task-events", append(batchSources, "server/managers/AudioMetadataManager.js:33-35"),
			"A batch refused while its member is processing emits no task_started or task_finished for the refused item.",
			func(t *testing.T, e *harness.Env) { checkBatchBusy(t, e, "reject-task-events") }),
		b(taskStarted, invTaskStart, "error", "changed", "rejected-admission-starts-no-task", append(batchSources, "server/controllers/ToolsController.js:137-140"),
			"A batch refused because a member is already processing starts no task for any of its members.",
			func(t *testing.T, e *harness.Env) { checkBatchBusy(t, e, "reject-task-started") }),
		b(taskFinished, invTaskFinish, "error", "changed", "rejected-admission-finishes-no-task", taskJSONSources,
			"A refused batch admission produces no task_finished for the refused member.",
			func(t *testing.T, e *harness.Env) { checkBatchBusy(t, e, "reject-task-finished") }),
		b(trackStarted, invTrackStart, "error", "changed", "rejected-admission-emits-no-track-events", append(batchSources, "server/managers/AudioMetadataManager.js:202-209"),
			"A refused batch admission produces no track_started for the refused member.",
			func(t *testing.T, e *harness.Env) { checkBatchBusy(t, e, "reject-track-started") }),
		b(trackProgress, invTrackProg, "error", "changed", "rejected-admission-emits-no-track-progress", progressSources,
			"A refused batch admission produces no track_progress for the refused member.",
			func(t *testing.T, e *harness.Env) { checkBatchBusy(t, e, "reject-track-progress") }),
		b(taskProgress, "INV-SOCKET-OUTGOING-TASK-PROGRESS", "error", "changed", "rejected-admission-emits-no-task-progress", progressSources,
			"A refused batch admission produces no task_progress for the refused member.",
			func(t *testing.T, e *harness.Env) { checkBatchBusy(t, e, "reject-task-progress") }),
		b(trackFinished, invTrackFin, "error", "changed", "rejected-admission-emits-no-track-finished", trackAMMSources,
			"A refused batch admission produces no track_finished for the refused member.",
			func(t *testing.T, e *harness.Env) { checkBatchBusy(t, e, "reject-track-finished") }),
		b(capTrackFin, invCapTrackF, "error", "changed", "cap-rejected-admission-emits-no-track-finished", append(trackAMMSources, "server/controllers/ToolsController.js:137-140"),
			"A batch admission refused while a member is processing emits no track_finished for the refused member.",
			func(t *testing.T, e *harness.Env) { checkBatchBusy(t, e, "cap-reject-track-finished") }),
		b(capTrackProg, invCapTrackP, "error", "changed", "rejected-admission-emits-no-progress", append(progressSources, "server/controllers/ToolsController.js:137-140"),
			"A batch admission refused while a member is processing emits neither task_progress nor track_progress for the refused member.",
			func(t *testing.T, e *harness.Env) { checkBatchBusy(t, e, "cap-reject-progress") }),

		// A missing item (a persisted flag only the watcher normally produces)
		// answers the routes' not-found and invalid-item errors.
		b(r183, "INV-ROUTE-R183-ROUTE", "error", "changed", "missing-item-answers-defined-errors", append(encodeSources, "server/controllers/ToolsController.js:85-88", "server/controllers/ToolsController.js:132-135"),
			"With the item's persisted isMissing flag set, the encode route answers 404 'Audiobook not found' before any other check and the embed and batch routes answer 400.",
			func(t *testing.T, e *harness.Env) { checkMissingItem(t, e, "missing-item") }),
	}
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
