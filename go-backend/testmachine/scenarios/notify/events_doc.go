package notify

// notificationDataDocument is the literal notificationData object of
// server/utils/notifications.js: the event catalogue NotificationManager.getData
// serves to the notification settings page and its edit modal. onTest carries
// the running server version, so it is passed in.
func notificationDataDocument(version string) map[string]any {
	return map[string]any{
		"events": []any{
			map[string]any{
				"name":             "onPodcastEpisodeDownloaded",
				"requiresLibrary":  true,
				"libraryMediaType": "podcast",
				"description":      "Triggered when a podcast episode is auto-downloaded",
				"descriptionKey":   "NotificationOnEpisodeDownloadedDescription",
				"variables":        []any{"libraryItemId", "libraryId", "podcastTitle", "podcastAuthor", "podcastDescription", "podcastGenres", "episodeTitle", "episodeSubtitle", "episodeDescription", "libraryName", "episodeId", "mediaTags"},
				"defaults":         map[string]any{"title": "New {{podcastTitle}} Episode!", "body": "{{episodeTitle}} has been added to {{libraryName}} library."},
				"testData":         map[string]any{"libraryItemId": "li_notification_test", "libraryId": "lib_test", "libraryName": "Podcasts", "mediaTags": "TestTag1, TestTag2", "podcastTitle": "Abs Test Podcast", "podcastAuthor": "Audiobookshelf", "podcastDescription": "Description of the Abs Test Podcast belongs here.", "podcastGenres": "TestGenre1, TestGenre2", "episodeId": "ep_notification_test", "episodeTitle": "Successful Test Episode", "episodeSubtitle": "Episode Subtitle", "episodeDescription": "Some description of the podcast episode."},
			},
			map[string]any{
				"name":            "onBackupCompleted",
				"requiresLibrary": false,
				"description":     "Triggered when a backup is completed",
				"descriptionKey":  "NotificationOnBackupCompletedDescription",
				"variables":       []any{"completionTime", "backupPath", "backupSize", "backupCount", "removedOldest"},
				"defaults":        map[string]any{"title": "Backup Completed", "body": "Backup has been completed successfully.\n\nPath: {{backupPath}}\nSize: {{backupSize}} bytes\nCount: {{backupCount}}\nRemoved Oldest: {{removedOldest}}"},
				"testData":        map[string]any{"completionTime": "12:00 AM", "backupPath": "path/to/backup", "backupSize": "1.23 MB", "backupCount": "1", "removedOldest": "false"},
			},
			map[string]any{
				"name":            "onBackupFailed",
				"requiresLibrary": false,
				"description":     "Triggered when a backup fails",
				"descriptionKey":  "NotificationOnBackupFailedDescription",
				"variables":       []any{"errorMsg"},
				"defaults":        map[string]any{"title": "Backup Failed", "body": "Backup failed, check ABS logs for more information.\nError message: {{errorMsg}}"},
				"testData":        map[string]any{"errorMsg": "Example error message"},
			},
			map[string]any{
				"name":            "onRSSFeedFailed",
				"requiresLibrary": true,
				"description":     "Triggered when the RSS feed request fails for an automatic episode download",
				"descriptionKey":  "NotificationOnRSSFeedFailedDescription",
				"variables":       []any{"feedUrl", "numFailed", "title"},
				"defaults":        map[string]any{"title": "RSS Feed Request Failed", "body": "Failed to request RSS feed for {{title}}.\nFeed URL: {{feedUrl}}\nNumber of failed attempts: {{numFailed}}"},
				"testData":        map[string]any{"title": "Test RSS Feed", "feedUrl": "https://example.com/rss", "numFailed": 3},
			},
			map[string]any{
				"name":            "onRSSFeedDisabled",
				"requiresLibrary": true,
				"description":     "Triggered when automatic episode downloads are disabled due to too many failed attempts",
				"descriptionKey":  "NotificationOnRSSFeedDisabledDescription",
				"variables":       []any{"feedUrl", "numFailed", "title"},
				"defaults":        map[string]any{"title": "Podcast Episode Download Schedule Disabled", "body": "Automatic episode downloads for {{title}} have been disabled due to too many failed RSS feed requests.\nFeed URL: {{feedUrl}}\nNumber of failed attempts: {{numFailed}}"},
				"testData":        map[string]any{"title": "Test RSS Feed", "feedUrl": "https://example.com/rss", "numFailed": 5},
			},
			map[string]any{
				"name":            "onTest",
				"requiresLibrary": false,
				"description":     "Event for testing the notification system",
				"descriptionKey":  "NotificationOnTestDescription",
				"variables":       []any{"version"},
				"defaults":        map[string]any{"title": "Test Notification on Abs {{version}}", "body": "Test notification body for abs {{version}}."},
				"testData":        map[string]any{"version": "v" + version},
			},
		},
	}
}

// defaultSettingsDocument is the settings object of a fresh install:
// NotificationSettings.construct runs without stored settings, so every default
// of server/objects/settings/NotificationSettings.js:5-19 applies.
func defaultSettingsDocument() map[string]any {
	return map[string]any{
		"id":                   "notification-settings",
		"appriseType":          "api",
		"appriseApiUrl":        nil,
		"notifications":        []any{},
		"maxFailedAttempts":    5,
		"maxNotificationQueue": 20,
		"notificationDelay":    1000,
	}
}

// recordDocument renders one notification record exactly as
// Notification.toJSON does, with the delivery counters the caller observed.
func recordDocument(record notificationRecord) map[string]any {
	urls := make([]any, len(record.URLs))
	for i, value := range record.URLs {
		urls[i] = value
	}
	return map[string]any{
		"id":                           record.ID,
		"libraryId":                    record.LibraryID,
		"eventName":                    record.EventName,
		"urls":                         urls,
		"titleTemplate":                record.TitleTemplate,
		"bodyTemplate":                 record.BodyTemplate,
		"enabled":                      record.Enabled,
		"type":                         record.Type,
		"lastFiredAt":                  record.LastFiredAt,
		"lastAttemptFailed":            record.LastAttemptFailed,
		"numConsecutiveFailedAttempts": record.NumConsecutiveFailedAttempts,
		"numTimesFired":                record.NumTimesFired,
		"createdAt":                    record.CreatedAt,
	}
}

// settingsDocumentOf renders the whole settings document with the records in
// order, for an exact comparison with an emitted payload.
func settingsDocumentOf(settings settingsDocument) map[string]any {
	records := make([]any, len(settings.Notifications))
	for i, record := range settings.Notifications {
		records[i] = recordDocument(record)
	}
	return map[string]any{
		"id":                   settings.ID,
		"appriseType":          settings.AppriseType,
		"appriseApiUrl":        settings.AppriseAPIURL,
		"notifications":        records,
		"maxFailedAttempts":    settings.MaxFailedAttempts,
		"maxNotificationQueue": settings.MaxNotificationQueue,
		"notificationDelay":    settings.NotificationDelay,
	}
}
