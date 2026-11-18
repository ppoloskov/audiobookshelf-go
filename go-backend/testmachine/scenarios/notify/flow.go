package notify

import (
	"net/http"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	notifyFlow       = "cap-flow-notify-delivery"
	flowDelivery     = "INV-CAP-FLOW-NOTIFY-DELIVERY"
	flowAdmission    = "INV-CAP-FLOW-NOTIFY-DELIVERY-ADMISSION-001"
	flowFailure      = "INV-CAP-FLOW-NOTIFY-DELIVERY-FAILURE-001"
	flowManagerSrc   = "server/managers/NotificationManager.js:151-178"
	flowAdmitSrc     = "server/managers/NotificationManager.js:22-46"
	flowScheduleSrc  = "server/managers/PodcastManager.js:250-262"
	flowScheduleSrc2 = "server/managers/PodcastManager.js:320-375"
	flowSettingsSrc  = "server/objects/settings/NotificationSettings.js:41-59"
	flowRecordSrc    = "server/objects/Notification.js:97-102"
	flowPayloadSrc   = "server/objects/Notification.js:104-131"
)

// flowScenarios covers the delivery flow of admitted notification triggers:
// the scheduler's podcast download, the enabled/matching admission rules and the
// failure, retry and disable transitions persisted in the settings document.
func flowScenarios() []harness.Scenario {
	return []harness.Scenario{
		scenario(notifyFlow, flowDelivery, "normal", "initial", "notify-flow-real-trigger-delivers",
			"When the podcast scheduler auto-downloads an episode, the manager delivers one Apprise payload for the enabled onPodcastEpisodeDownloaded subscription rendered from the downloaded episode and the item metadata, and records one firing without a failed attempt.",
			[]string{flowManagerSrc, flowAdmitSrc, flowScheduleSrc, flowScheduleSrc2, flowPayloadSrc, "server/utils/notifications.js:6-40"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				feed := newFeedStub(t)
				feed.setAudio("/episode.mp3", fixtureEpisodeAudio(t, e))
				feed.setFeed("/feed.xml", rssDocument("Notify Cast", stubItem{
					Title:           "Notify Episode One",
					Subtitle:        "Episode Subtitle",
					Description:     "<p>Notify episode description</p>",
					PubDate:         episodePubDate,
					GUID:            "notify-episode-one",
					EnclosureURL:    feed.url("/episode.mp3"),
					EnclosureType:   "audio/mpeg",
					EnclosureLength: "96673",
				}))
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				urls := []string{"mailto://podcast@example.test"}
				_, record := createRecord(t, e, map[string]any{
					"libraryId": nil, "eventName": "onPodcastEpisodeDownloaded", "urls": urls,
					"titleTemplate": "New {{podcastTitle}} Episode!",
					"bodyTemplate":  "{{podcastTitle}}|{{libraryName}}|{{mediaTags}}|{{podcastGenres}}|{{podcastAuthor}}|{{episodeTitle}}|{{episodeSubtitle}}|{{episodeDescription}}",
					"enabled":       true, "type": nil,
				})
				before := time.Now()
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				itemID := createAutoDownloadPodcast(t, e, "Notify Cast", feed.url("/feed.xml"))
				t.Cleanup(func() {
					// Stop the scheduler even when an assertion below fails.
					resp := e.Patch(t, e.Token(t, "root"), "/api/items/"+itemID+"/media", map[string]any{"autoDownloadEpisodes": false})
					if resp.Status != http.StatusOK {
						t.Logf("cleanup: disabling auto download answered %d", resp.Status)
					}
				})
				call := stub.wait(t, 1, triggerWait)[0]
				call.requireDelivery(t, urls,
					"New Notify Cast Episode!",
					"Notify Cast|Podcasts|TagA, TagB|GenreX, GenreY|Notify Author|Notify Episode One|Episode Subtitle|<p>Notify episode description</p>")
				if feed.hitsOf("/feed.xml") == 0 {
					t.Fatal("the scheduler never fetched the stub feed")
				}
				if stored := e.Count(t, "SELECT count(*) FROM podcastEpisodes WHERE title = ?", "Notify Episode One"); stored != 1 {
					t.Fatalf("stored episodes titled Notify Episode One = %d, want 1", stored)
				}
				// The event proves the manager finished the delivery and persisted it.
				socket.Wait(t, mark, "notifications_updated", triggerWait, nil)
				fired := requireRecord(t, getNotifications(t, e).Settings, record.ID)
				if fired.NumTimesFired != 1 || fired.NumConsecutiveFailedAttempts != 0 || fired.LastAttemptFailed || !fired.Enabled {
					t.Fatalf("record after the scheduled delivery = %+v", fired)
				}
				requireTimestamp(t, "lastFiredAt", fired.LastFiredAt, before)
				requireJSONEqual(t, "persisted settings", mustStoredValue(t, e), settingsDocumentOf(getNotifications(t, e).Settings))
				stub.requireExactly(t, 1)
			}),
		scenario(notifyFlow, flowAdmission, "edge", "changed", "notify-flow-real-trigger-with-no-subscriber",
			"When the scheduler auto-downloads an episode while no enabled onPodcastEpisodeDownloaded subscription exists, the episode is stored, the manager returns before the queue and the emitter, and nothing is delivered or emitted.",
			[]string{flowAdmitSrc, flowScheduleSrc2, eventEmitSrc},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				feed := newFeedStub(t)
				feed.setAudio("/episode.mp3", fixtureEpisodeAudio(t, e))
				feed.setFeed("/feed.xml", rssDocument("Quiet Cast", stubItem{
					Title:           "Quiet Episode One",
					PubDate:         episodePubDate,
					GUID:            "quiet-episode-one",
					EnclosureURL:    feed.url("/episode.mp3"),
					EnclosureType:   "audio/mpeg",
					EnclosureLength: "96673",
				}))
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				_, other := createRecord(t, e, recordBody("onTest", true))
				socket := e.Socket(t, e.Token(t, "root"))
				mark := socket.Mark()
				itemID := createAutoDownloadPodcast(t, e, "Quiet Cast", feed.url("/feed.xml"))
				t.Cleanup(func() {
					resp := e.Patch(t, e.Token(t, "root"), "/api/items/"+itemID+"/media", map[string]any{"autoDownloadEpisodes": false})
					if resp.Status != http.StatusOK {
						t.Logf("cleanup: disabling auto download answered %d", resp.Status)
					}
				})
				harness.Eventually(t, triggerWait, "the scheduled episode download", func() bool {
					return e.Count(t, "SELECT count(*) FROM podcastEpisodes WHERE title = ?", "Quiet Episode One") == 1
				})
				socket.None(t, mark, "notifications_updated", quietWindow, nil)
				stub.requireNoDelivery(t, "a scheduled trigger without a matching subscription")
				kept := requireRecord(t, getNotifications(t, e).Settings, other.ID)
				if kept.NumTimesFired != 0 || kept.LastFiredAt != nil {
					t.Fatalf("the unrelated onTest record fired: %+v", kept)
				}
			}),
		scenario(notifyFlow, flowDelivery, "normal", "repeat", "notify-flow-only-enabled-matching-subscriptions",
			"Firing onTest delivers only to the enabled subscription of that event: the disabled onTest record and the enabled record of another event receive no request and keep their counters.",
			[]string{flowManagerSrc, flowSettingsSrc, "server/objects/settings/NotificationSettings.js:57-59"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				enabledURLs := []string{"mailto://enabled@example.test"}
				_, enabled := createRecord(t, e, map[string]any{
					"libraryId": nil, "eventName": "onTest", "urls": enabledURLs,
					"titleTemplate": "Delivered", "bodyTemplate": "Delivered body", "enabled": true, "type": nil,
				})
				_, disabled := createRecord(t, e, map[string]any{
					"libraryId": nil, "eventName": "onTest", "urls": []string{"mailto://disabled@example.test"},
					"titleTemplate": "Disabled", "bodyTemplate": "Disabled body", "enabled": false, "type": nil,
				})
				_, otherEvent := createRecord(t, e, map[string]any{
					"libraryId": nil, "eventName": "onBackupFailed", "urls": []string{"mailto://other@example.test"},
					"titleTemplate": "Other", "bodyTemplate": "Other body", "enabled": true, "type": nil,
				})
				fireTriggerSynced(t, e, "0")
				stub.waitCalls(t, 1)[0].requireDelivery(t, enabledURLs, "Delivered", "Delivered body")
				stub.requireExactly(t, 1)
				settings := getNotifications(t, e).Settings
				for _, silent := range []notificationRecord{disabled, otherEvent} {
					kept := requireRecord(t, settings, silent.ID)
					if kept.NumTimesFired != 0 || kept.LastFiredAt != nil || kept.NumConsecutiveFailedAttempts != 0 {
						t.Fatalf("unmatched record %s received a delivery: %+v", kept.EventName, kept)
					}
				}
				if fired := requireRecord(t, settings, enabled.ID); fired.NumTimesFired != 1 {
					t.Fatalf("enabled record numTimesFired = %d, want 1", fired.NumTimesFired)
				}
			}),
		scenario(notifyFlow, flowAdmission, "edge", "initial", "notify-flow-no-active-subscription-sends-nothing",
			"With only a disabled onTest record and an enabled record of another event, a test trigger answers 200, sends nothing and leaves both records without a firing.",
			[]string{flowManagerSrc, flowSettingsSrc},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				_, disabled := createRecord(t, e, map[string]any{
					"libraryId": nil, "eventName": "onTest", "urls": []string{"mailto://off@example.test"},
					"titleTemplate": "Off", "bodyTemplate": "Off body", "enabled": false, "type": nil,
				})
				_, other := createRecord(t, e, recordBody("onRSSFeedFailed", true))
				stamp := takeSettingsSnapshot(t, e)
				fireTriggerSynced(t, e, "0")
				stub.requireNoDelivery(t, "a trigger with no active subscription")
				for _, record := range []notificationRecord{disabled, other} {
					kept := requireRecord(t, getNotifications(t, e).Settings, record.ID)
					if kept.NumTimesFired != 0 || kept.LastFiredAt != nil {
						t.Fatalf("record %s fired: %+v", kept.EventName, kept)
					}
				}
				requireUnchangedValue(t, e, stamp.document, "a trigger with no active subscription")
			}),
		scenario(notifyFlow, flowAdmission, "edge", "failure", "notify-flow-unusable-settings-skip-everything",
			"Without an Apprise URL an enabled subscription makes the test trigger answer 200, send nothing and leave the settings document and its row untouched.",
			[]string{flowManagerSrc, flowSettingsSrc, "server/objects/settings/NotificationSettings.js:41-43"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				useApprise(t, e, "", 5, 20)
				clearRecords(t, e)
				createRecord(t, e, recordBody("onTest", true))
				stamp := takeSettingsSnapshot(t, e)
				if stamp.document["appriseApiUrl"] != nil {
					t.Fatalf("precondition: appriseApiUrl = %v, want null", stamp.document["appriseApiUrl"])
				}
				wantStatus(t, "trigger without an Apprise URL", triggerTest(t, e, "0"), http.StatusOK)
				stub.requireNoDelivery(t, "an unusable notification system")
				stamp.requireUnchanged(t, e, "a trigger on unusable settings")
			}),
		scenario(notifyFlow, flowFailure, "error", "failure", "notify-flow-failure-counts-then-disables",
			"An Apprise 503 counts consecutive failed attempts, disables the subscription once maxFailedAttempts is reached, and the disabled record then receives no further attempt.",
			[]string{flowManagerSrc, flowRecordSrc, "server/managers/NotificationManager.js:163-171"},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				stub.respond(http.StatusServiceUnavailable, 0)
				useApprise(t, e, stub.URL("/notify"), 2, 20)
				clearRecords(t, e)
				_, record := createRecord(t, e, recordBody("onTest", true))
				fireTriggerSynced(t, e, "0")
				first := requireRecord(t, getNotifications(t, e).Settings, record.ID)
				if !first.Enabled || first.NumConsecutiveFailedAttempts != 1 || !first.LastAttemptFailed || first.NumTimesFired != 1 {
					t.Fatalf("record after one failure = %+v", first)
				}
				fireTriggerSynced(t, e, "0")
				second := requireRecord(t, getNotifications(t, e).Settings, record.ID)
				if second.Enabled || second.NumConsecutiveFailedAttempts != 2 || !second.LastAttemptFailed || second.NumTimesFired != 2 {
					t.Fatalf("record after two failures = %+v", second)
				}
				stub.requireExactly(t, 2)
				fireTriggerSynced(t, e, "0")
				time.Sleep(quietWindow)
				if got := stub.Count(); got != 2 {
					t.Fatalf("a disabled subscription received %d deliveries in total, want the 2 failed attempts", got)
				}
				persisted := requireRecord(t, storedDocument(t, e), record.ID)
				if persisted.Enabled || persisted.NumConsecutiveFailedAttempts != 2 || persisted.NumTimesFired != 2 || !persisted.LastAttemptFailed {
					t.Fatalf("persisted record = %+v", persisted)
				}
			}),
		scenario(notifyFlow, flowFailure, "normal", "changed", "notify-flow-success-resets-consecutive-failures",
			"A successful delivery after one failed attempt clears lastAttemptFailed and the consecutive failure count while numTimesFired keeps counting.",
			[]string{flowManagerSrc, flowRecordSrc},
			func(t *testing.T, e *harness.Env) {
				stub := newAppriseStub(t)
				stub.respond(http.StatusInternalServerError, 0)
				useApprise(t, e, stub.URL("/notify"), 5, 20)
				clearRecords(t, e)
				urls := []string{"mailto://recover@example.test"}
				_, record := createRecord(t, e, map[string]any{
					"libraryId": nil, "eventName": "onTest", "urls": urls,
					"titleTemplate": "Recovered", "bodyTemplate": "Recovered body", "enabled": true, "type": nil,
				})
				fireTriggerSynced(t, e, "0")
				failed := requireRecord(t, getNotifications(t, e).Settings, record.ID)
				if failed.NumConsecutiveFailedAttempts != 1 || !failed.LastAttemptFailed || !failed.Enabled {
					t.Fatalf("record after the failure = %+v", failed)
				}
				stub.respond(http.StatusOK, 0)
				fireTriggerSynced(t, e, "0")
				recovered := requireRecord(t, getNotifications(t, e).Settings, record.ID)
				if recovered.NumConsecutiveFailedAttempts != 0 || recovered.LastAttemptFailed || recovered.NumTimesFired != 2 || recovered.LastFiredAt == nil {
					t.Fatalf("record after the successful retry = %+v", recovered)
				}
				calls := stub.requireExactly(t, 2)
				calls[1].requireDelivery(t, urls, "Recovered", "Recovered body")
			}),
	}
}
