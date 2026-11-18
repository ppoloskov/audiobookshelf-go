package user_me

import "audiobookshelf-compat/tests/replacement/harness"

func strp(s string) *string   { return &s }
func f64p(f float64) *float64 { return &f }

// listenerProgress returns the listener's seeded media progress rows as the
// clients see them, keyed by progress id. lastUpdate and startedAt are the
// seeded row timestamps in milliseconds.
func listenerProgress(e *harness.Env) map[string]progressJSON {
	user := e.IDs["listener"]
	return map[string]progressJSON{
		"b95b3028-3461-4306-803b-6e310140cd22": {ID: "b95b3028-3461-4306-803b-6e310140cd22", UserID: user, LibraryItemID: e.IDs["saga1"], MediaItemID: "5e33de11-3e94-4c51-87d8-d06d2928ac38", MediaItemType: "book", Duration: 60.186123, Progress: 0.4569159571883372, CurrentTime: 27.5, LastUpdate: 1793709986973, StartedAt: 1793709986942},
		"20ae1a03-2eac-4b4a-a595-6220a2f0b475": {ID: "20ae1a03-2eac-4b4a-a595-6220a2f0b475", UserID: user, LibraryItemID: e.IDs["novel"], MediaItemID: "85ab0f32-b17f-4c9a-883e-a61e260f1e46", MediaItemType: "book", Progress: 1, IsFinished: true, LastUpdate: 1793709987019, StartedAt: 1793709987019, FinishedAt: f64p(1793709987019)},
		"c896481a-a447-4bf2-ab05-0f6a40906b03": {ID: "c896481a-a447-4bf2-ab05-0f6a40906b03", UserID: user, LibraryItemID: e.IDs["ebook"], MediaItemID: "83013005-1614-4732-8f4b-07858cfb5de0", MediaItemType: "book", EbookLocation: strp("epubcfi(/6/2!/4/2/1:0)"), EbookProgress: 0.25, LastUpdate: 1793709987050, StartedAt: 1793709987049},
		"52ee48a5-8539-4635-871e-b649103fa0f5": {ID: "52ee48a5-8539-4635-871e-b649103fa0f5", UserID: user, LibraryItemID: e.IDs["saga2"], MediaItemID: "5c5badbc-e141-4fbc-8a5b-31c1ba7d33af", MediaItemType: "book", Duration: 30, Progress: 0.3, CurrentTime: 9, LastUpdate: 1793709987379, StartedAt: 1793709987379},
		"8b1b3f1b-dd6f-4809-ade0-eeef6ff56c98": {ID: "8b1b3f1b-dd6f-4809-ade0-eeef6ff56c98", UserID: user, LibraryItemID: e.IDs["podcast"], EpisodeID: strp(e.IDs["ep1"]), MediaItemID: e.IDs["ep1"], MediaItemType: "podcastEpisode", Duration: 24.058776, Progress: 0.4364311800400818, CurrentTime: 10.5, LastUpdate: 1793709988133, StartedAt: 1793709988028},
		"e83ed67e-8012-408d-a9a1-475e6fd83828": {ID: "e83ed67e-8012-408d-a9a1-475e6fd83828", UserID: user, LibraryItemID: e.IDs["podcast"], EpisodeID: strp(e.IDs["ep2"]), MediaItemID: e.IDs["ep2"], MediaItemType: "podcastEpisode", Progress: 1, IsFinished: true, LastUpdate: 1793709988179, StartedAt: 1793709988178, FinishedAt: f64p(1793709988178)},
		"3dc1ddba-4b1a-45fc-8b91-53e355fc52fc": {ID: "3dc1ddba-4b1a-45fc-8b91-53e355fc52fc", UserID: user, LibraryItemID: e.IDs["podcast"], EpisodeID: strp(e.IDs["ep3"]), MediaItemID: e.IDs["ep3"], MediaItemType: "podcastEpisode", Duration: 12.068571, Progress: 0.4971591085638888, CurrentTime: 6, LastUpdate: 1793709988245, StartedAt: 1793709988244},
	}
}

// finisherSaga1Progress is the finisher's only seeded progress row.
func finisherSaga1Progress(e *harness.Env) progressJSON {
	return progressJSON{ID: finisherProgress, UserID: finisherID, LibraryItemID: e.IDs["saga1"], MediaItemID: "5e33de11-3e94-4c51-87d8-d06d2928ac38", MediaItemType: "book", Progress: 1, IsFinished: true, LastUpdate: 1793709987294, StartedAt: 1793709987294, FinishedAt: f64p(1793709987294)}
}

// listenerBookmarks returns the listener's seeded bookmarks in stored order.
func listenerBookmarks(e *harness.Env) []bookmarkJSON {
	return []bookmarkJSON{
		{LibraryItemID: e.IDs["saga1"], Time: 21, Title: "Good bit", CreatedAt: 1793709987082},
		{LibraryItemID: e.IDs["podcast"], Time: 5, Title: "Cast bookmark", CreatedAt: 1793709988272},
	}
}
