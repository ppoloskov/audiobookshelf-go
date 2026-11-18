package item_write

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

func mediaPodcastScenarios(s *setup) []harness.Scenario {
	const inv = "INV-R053-PODCAST-META-001"
	return []harness.Scenario{
		scenario(r053, inv, "normal", "initial", "podcast-all-metadata-fields",
			"PATCH media of a podcast with every field of the web podcast edit form stores each value in the podcasts row (feedURL, imageURL, itunes ids, podcastType), answers them in the old-format item and rewrites metadata.json.",
			srcs(updateMediaSrc, podcastUpdateSrc, metadataFileSrc, "client/components/widgets/PodcastDetailsEdit.vue:201-240"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["podcast"]
				row := mediaRow(t, e, id)
				if row["id"] != "a477b353-4058-4b9e-8399-49781256fc24" || row["title"] != "Fixture Cast" || row["author"] != "Fixture Host" || row["podcastType"] != "episodic" {
					t.Fatalf("fixture podcast %v", row)
				}
				before := updatedAt(t, e, id)
				body := map[string]any{
					"metadata": map[string]any{
						"title": "A Edited Cast", "author": "Edited Host", "description": "<p>About the cast</p>", "releaseDate": "2024-05-06",
						"genres": []string{"News", "Comedy"}, "feedUrl": "https://feeds.example.invalid/cast.xml", "imageUrl": "https://img.example.invalid/c.jpg",
						"itunesPageUrl": "https://itunes.example.invalid/p", "itunesId": "12345", "itunesArtistId": "67890", "explicit": true,
						"language": "fr", "type": "serial",
					},
					"tags": []string{"pod-tag"},
				}
				result := patchOK(t, e, id, body)
				if !result.Updated {
					t.Fatal("updated = false")
				}
				columns := map[string]any{
					"title": "A Edited Cast", "titleIgnorePrefix": "Edited Cast", "author": "Edited Host", "description": "<p>About the cast</p>", "releaseDate": "2024-05-06",
					"feedURL": "https://feeds.example.invalid/cast.xml", "imageURL": "https://img.example.invalid/c.jpg", "itunesPageURL": "https://itunes.example.invalid/p",
					"itunesId": "12345", "itunesArtistId": "67890", "language": "fr", "podcastType": "serial", "explicit": int64(1),
				}
				stored := mediaRow(t, e, id)
				for key, want := range columns {
					if stored[key] != want {
						t.Fatalf("podcasts.%s = %v, want %v", key, stored[key], want)
					}
				}
				requireEqual(t, "podcasts.genres", column(t, stored, "genres"), []any{"News", "Comedy"})
				requireEqual(t, "podcasts.tags", column(t, stored, "tags"), []any{"pod-tag"})
				metadata := metadataOf(result.LibraryItem)
				for key, want := range map[string]any{
					"title": "A Edited Cast", "author": "Edited Host", "description": "<p>About the cast</p>", "releaseDate": "2024-05-06",
					"feedUrl": "https://feeds.example.invalid/cast.xml", "imageUrl": "https://img.example.invalid/c.jpg", "itunesPageUrl": "https://itunes.example.invalid/p",
					"itunesId": "12345", "itunesArtistId": "67890", "language": "fr", "type": "serial", "explicit": true,
				} {
					if metadata[key] != want {
						t.Fatalf("response metadata.%s = %v, want %v", key, metadata[key], want)
					}
				}
				requireEqual(t, "response genres", stringList(metadata["genres"]), []string{"News", "Comedy"})
				if item := itemRow(t, e, id); item["title"] != "A Edited Cast" || item["titleIgnorePrefix"] != "Edited Cast" {
					t.Fatalf("libraryItems title %v / %v", item["title"], item["titleIgnorePrefix"])
				}
				requireEqual(t, "metadata.json", readMetadataFile(t, e, id), map[string]any{
					"tags": []any{"pod-tag"}, "title": "A Edited Cast", "author": "Edited Host", "description": "<p>About the cast</p>", "releaseDate": "2024-05-06",
					"genres": []any{"News", "Comedy"}, "feedURL": "https://feeds.example.invalid/cast.xml", "imageURL": "https://img.example.invalid/c.jpg",
					"itunesPageURL": "https://itunes.example.invalid/p", "itunesId": "12345", "itunesArtistId": "67890", "language": "fr", "explicit": true, "podcastType": "serial",
				})
				wantMoved(t, e, id, before)
			}),
		scenario(r053, inv, "normal", "changed", "podcast-download-settings",
			"The schedule form fields (autoDownloadEpisodes, autoDownloadSchedule, maxEpisodesToKeep, maxNewEpisodesToDownload, lastEpisodeCheck) are stored in the podcasts row and answered by the item.",
			srcs(updateMediaSrc, podcastUpdateSrc, "client/components/modals/item/tabs/Schedule.vue:140-156", "client/components/modals/item/tabs/Episodes.vue:111"),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["podcast"]
				before := updatedAt(t, e, id)
				result := patchOK(t, e, id, map[string]any{
					"autoDownloadEpisodes": true, "autoDownloadSchedule": "30 2 * * *", "maxEpisodesToKeep": 7, "maxNewEpisodesToDownload": 4,
					"lastEpisodeCheck": 1700000000000,
				})
				if !result.Updated {
					t.Fatal("updated = false")
				}
				row := mediaRow(t, e, id)
				if row["autoDownloadEpisodes"] != int64(1) || row["autoDownloadSchedule"] != "30 2 * * *" || row["maxEpisodesToKeep"] != int64(7) || row["maxNewEpisodesToDownload"] != int64(4) {
					t.Fatalf("podcasts row %v", row)
				}
				if row["lastEpisodeCheck"] != "2023-11-14 22:13:20.000 +00:00" {
					t.Fatalf("lastEpisodeCheck = %v", row["lastEpisodeCheck"])
				}
				media, _ := result.LibraryItem["media"].(map[string]any)
				if media["autoDownloadEpisodes"] != true || media["autoDownloadSchedule"] != "30 2 * * *" || media["maxEpisodesToKeep"] != float64(7) ||
					media["maxNewEpisodesToDownload"] != float64(4) || media["lastEpisodeCheck"] != float64(1700000000000) {
					t.Fatalf("response media %v", media)
				}
				wantMoved(t, e, id, before)
			}),
		scenario(r053, inv, "normal", "repeat", "podcast-repeat-is-noop",
			"Repeating the same podcast settings answers updated false and leaves updatedAt and rows untouched.",
			srcs(updateMediaSrc, podcastUpdateSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["podcast"]
				before, rows := updatedAt(t, e, id), snapshot(t, e)
				result := patchOK(t, e, id, map[string]any{"autoDownloadEpisodes": true, "autoDownloadSchedule": "30 2 * * *", "maxEpisodesToKeep": 7, "metadata": map[string]any{"title": "A Edited Cast", "type": "serial"}})
				if result.Updated {
					t.Fatal("updated = true")
				}
				wantUnchanged(t, e, id, before)
				wantSnapshot(t, e, rows)
			}),
		scenario(r053, inv, "error", "failure", "podcast-invalid-cron-is-rejected-first",
			"An invalid autoDownloadSchedule answers 400 before any field is applied: a title sent with it is not stored and no event is emitted.",
			srcs(updateMediaSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["podcast"]
				a := connect(t, e, map[string]string{"root": e.Token(t, "root")})
				before, rows := updatedAt(t, e, id), snapshot(t, e)
				resp := patchMedia(t, e, e.Token(t, "root"), id, map[string]any{"autoDownloadSchedule": "not a cron", "metadata": map[string]any{"title": "Never Stored"}})
				wantStatus(t, "invalid cron", resp, http.StatusBadRequest)
				wantUnchanged(t, e, id, before)
				wantSnapshot(t, e, rows)
				a.receive(t, "item_updated", idOf(id), nil, []string{"root"})
			}),
		scenario(r053, inv, "edge", "initial", "podcast-ignores-book-only-keys",
			"Authors and series in a podcast payload are ignored: no author or series rows are created and nothing is updated.",
			srcs(updateMediaSrc),
			func(t *testing.T, e *harness.Env) {
				id := e.IDs["podcast"]
				before, rows := updatedAt(t, e, id), snapshot(t, e)
				result := patchOK(t, e, id, map[string]any{"metadata": map[string]any{"authors": []any{named("Podcast Author")}, "series": []any{seriesEntry("Podcast Series", "1")}}})
				if result.Updated {
					t.Fatal("updated = true")
				}
				wantUnchanged(t, e, id, before)
				wantSnapshot(t, e, rows)
			}),
	}
}
