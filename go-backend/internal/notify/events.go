package notify

import (
	"slices"

	"audiobookshelf-compat/internal/version"
)

// Event describes one notification event for the client's notification editor.
type Event struct {
	Name             string         `json:"name"`
	RequiresLibrary  bool           `json:"requiresLibrary"`
	LibraryMediaType string         `json:"libraryMediaType,omitempty"`
	Description      string         `json:"description"`
	DescriptionKey   string         `json:"descriptionKey"`
	Variables        []string       `json:"variables"`
	Defaults         Template       `json:"defaults"`
	TestData         map[string]any `json:"testData"` // open-ended: values are strings or numbers per event
}

// Template is a title and body template pair.
type Template struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// catalogue is server/utils/notifications.js (read-only).
var catalogue = []Event{
	{Name: "onPodcastEpisodeDownloaded", RequiresLibrary: true, LibraryMediaType: "podcast",
		Description: "Triggered when a podcast episode is auto-downloaded", DescriptionKey: "NotificationOnEpisodeDownloadedDescription",
		Variables: []string{"libraryItemId", "libraryId", "podcastTitle", "podcastAuthor", "podcastDescription", "podcastGenres", "episodeTitle", "episodeSubtitle", "episodeDescription", "libraryName", "episodeId", "mediaTags"},
		Defaults:  Template{"New {{podcastTitle}} Episode!", "{{episodeTitle}} has been added to {{libraryName}} library."},
		TestData: map[string]any{"libraryItemId": "li_notification_test", "libraryId": "lib_test", "libraryName": "Podcasts", "mediaTags": "TestTag1, TestTag2",
			"podcastTitle": "Abs Test Podcast", "podcastAuthor": "Audiobookshelf", "podcastDescription": "Description of the Abs Test Podcast belongs here.",
			"podcastGenres": "TestGenre1, TestGenre2", "episodeId": "ep_notification_test", "episodeTitle": "Successful Test Episode",
			"episodeSubtitle": "Episode Subtitle", "episodeDescription": "Some description of the podcast episode."}},
	{Name: "onBackupCompleted", Description: "Triggered when a backup is completed", DescriptionKey: "NotificationOnBackupCompletedDescription",
		Variables: []string{"completionTime", "backupPath", "backupSize", "backupCount", "removedOldest"},
		Defaults:  Template{"Backup Completed", "Backup has been completed successfully.\n\nPath: {{backupPath}}\nSize: {{backupSize}} bytes\nCount: {{backupCount}}\nRemoved Oldest: {{removedOldest}}"},
		TestData:  map[string]any{"completionTime": "12:00 AM", "backupPath": "path/to/backup", "backupSize": "1.23 MB", "backupCount": "1", "removedOldest": "false"}},
	{Name: "onBackupFailed", Description: "Triggered when a backup fails", DescriptionKey: "NotificationOnBackupFailedDescription",
		Variables: []string{"errorMsg"},
		Defaults:  Template{"Backup Failed", "Backup failed, check ABS logs for more information.\nError message: {{errorMsg}}"},
		TestData:  map[string]any{"errorMsg": "Example error message"}},
	{Name: "onRSSFeedFailed", RequiresLibrary: true, Description: "Triggered when the RSS feed request fails for an automatic episode download", DescriptionKey: "NotificationOnRSSFeedFailedDescription",
		Variables: []string{"feedUrl", "numFailed", "title"},
		Defaults:  Template{"RSS Feed Request Failed", "Failed to request RSS feed for {{title}}.\nFeed URL: {{feedUrl}}\nNumber of failed attempts: {{numFailed}}"},
		TestData:  map[string]any{"title": "Test RSS Feed", "feedUrl": "https://example.com/rss", "numFailed": 3}},
	{Name: "onRSSFeedDisabled", RequiresLibrary: true, Description: "Triggered when automatic episode downloads are disabled due to too many failed attempts", DescriptionKey: "NotificationOnRSSFeedDisabledDescription",
		Variables: []string{"feedUrl", "numFailed", "title"},
		Defaults:  Template{"Podcast Episode Download Schedule Disabled", "Automatic episode downloads for {{title}} have been disabled due to too many failed RSS feed requests.\nFeed URL: {{feedUrl}}\nNumber of failed attempts: {{numFailed}}"},
		TestData:  map[string]any{"title": "Test RSS Feed", "feedUrl": "https://example.com/rss", "numFailed": 5}},
	{Name: "onTest", Description: "Event for testing the notification system", DescriptionKey: "NotificationOnTestDescription",
		Variables: []string{"version"},
		Defaults:  Template{"Test Notification on Abs {{version}}", "Test notification body for abs {{version}}."},
		TestData:  map[string]any{"version": "v" + version.Server}},
}

func eventNamed(name string) (Event, bool) {
	i := slices.IndexFunc(catalogue, func(e Event) bool { return e.Name == name })
	if i < 0 {
		return Event{}, false
	}
	return catalogue[i], true
}
