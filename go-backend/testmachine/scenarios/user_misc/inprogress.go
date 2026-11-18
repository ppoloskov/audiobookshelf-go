package user_misc

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

// itemView is the minified library item projection of the items-in-progress
// route. Path is checked against e.Media and RelPath, not as a literal.
type itemView struct {
	ID                 string       `json:"id"`
	Ino                string       `json:"ino"`
	OldLibraryItemID   *string      `json:"oldLibraryItemId"`
	LibraryID          string       `json:"libraryId"`
	FolderID           string       `json:"folderId"`
	RelPath            string       `json:"relPath"`
	IsFile             bool         `json:"isFile"`
	MtimeMs            int64        `json:"mtimeMs"`
	CtimeMs            int64        `json:"ctimeMs"`
	BirthtimeMs        int64        `json:"birthtimeMs"`
	AddedAt            int64        `json:"addedAt"`
	UpdatedAt          int64        `json:"updatedAt"`
	IsMissing          bool         `json:"isMissing"`
	IsInvalid          bool         `json:"isInvalid"`
	MediaType          string       `json:"mediaType"`
	Media              mediaView    `json:"media"`
	NumFiles           int          `json:"numFiles"`
	Size               int64        `json:"size"`
	RecentEpisode      *episodeView `json:"recentEpisode"`
	ProgressLastUpdate int64        `json:"progressLastUpdate"`
}

// mediaView covers the fields of both media projections; a pointer is nil when
// the key is absent or null.
type mediaView struct {
	ID                       string       `json:"id"`
	Metadata                 metadataView `json:"metadata"`
	CoverPath                *string      `json:"coverPath"`
	Tags                     []string     `json:"tags"`
	Duration                 *float64     `json:"duration"`
	NumTracks                *int         `json:"numTracks"`
	NumAudioFiles            *int         `json:"numAudioFiles"`
	NumChapters              *int         `json:"numChapters"`
	Size                     *int64       `json:"size"`
	EbookFormat              *string      `json:"ebookFormat"`
	NumEpisodes              *int         `json:"numEpisodes"`
	AutoDownloadEpisodes     *bool        `json:"autoDownloadEpisodes"`
	AutoDownloadSchedule     *string      `json:"autoDownloadSchedule"`
	LastEpisodeCheck         *int64       `json:"lastEpisodeCheck"`
	MaxEpisodesToKeep        *int         `json:"maxEpisodesToKeep"`
	MaxNewEpisodesToDownload *int         `json:"maxNewEpisodesToDownload"`
}

// metadataView covers book and podcast metadata fields.
type metadataView struct {
	Title             string   `json:"title"`
	TitleIgnorePrefix *string  `json:"titleIgnorePrefix"`
	Subtitle          *string  `json:"subtitle"`
	AuthorName        *string  `json:"authorName"`
	AuthorNameLF      *string  `json:"authorNameLF"`
	NarratorName      *string  `json:"narratorName"`
	SeriesName        *string  `json:"seriesName"`
	Genres            []string `json:"genres"`
	PublishedYear     *string  `json:"publishedYear"`
	PublishedDate     *string  `json:"publishedDate"`
	Publisher         *string  `json:"publisher"`
	Description       *string  `json:"description"`
	ISBN              *string  `json:"isbn"`
	ASIN              *string  `json:"asin"`
	Language          *string  `json:"language"`
	Explicit          *bool    `json:"explicit"`
	Abridged          *bool    `json:"abridged"`
	Author            *string  `json:"author"`
	ReleaseDate       *string  `json:"releaseDate"`
	FeedURL           *string  `json:"feedUrl"`
	ImageURL          *string  `json:"imageUrl"`
	ItunesPageURL     *string  `json:"itunesPageUrl"`
	ItunesID          *string  `json:"itunesId"`
	ItunesArtistID    *string  `json:"itunesArtistId"`
	Type              *string  `json:"type"`
}

// episodeView is the recentEpisode projection of a podcast item.
type episodeView struct {
	LibraryItemID string         `json:"libraryItemId"`
	PodcastID     string         `json:"podcastId"`
	ID            string         `json:"id"`
	OldEpisodeID  *string        `json:"oldEpisodeId"`
	Index         *int           `json:"index"`
	Season        *string        `json:"season"`
	Episode       *string        `json:"episode"`
	EpisodeType   *string        `json:"episodeType"`
	Title         string         `json:"title"`
	Subtitle      *string        `json:"subtitle"`
	Description   *string        `json:"description"`
	Enclosure     any            `json:"enclosure"`
	GUID          *string        `json:"guid"`
	PubDate       *string        `json:"pubDate"`
	PublishedAt   *int64         `json:"publishedAt"`
	Chapters      []any          `json:"chapters"`
	AudioFile     *audioFileView `json:"audioFile"`
	AddedAt       int64          `json:"addedAt"`
	UpdatedAt     int64          `json:"updatedAt"`
}

// audioFileView is the subset of the episode audio file clients read.
type audioFileView struct {
	Filename *string `json:"filename"`
	Duration float64 `json:"duration"`
	Size     *int64  `json:"size"`
	MimeType string  `json:"mimeType"`
	Codec    string  `json:"codec"`
	Channels int     `json:"channels"`
}

// itemsResponse is the GET /api/me/items-in-progress body.
type itemsResponse struct {
	LibraryItems []json.RawMessage `json:"libraryItems"`
}

// item decodes one served raw item into the typed projection.
func (r itemsResponse) item(t *testing.T, index int, out *itemView) {
	t.Helper()
	if len(r.LibraryItems) <= index {
		t.Fatalf("only %d items in progress", len(r.LibraryItems))
	}
	if err := json.Unmarshal(r.LibraryItems[index], out); err != nil {
		t.Fatalf("item %d: %v", index, err)
	}
}

func sp(s string) *string     { return &s }
func ip(i int) *int           { return &i }
func i64p(i int64) *int64     { return &i }
func f64p(f float64) *float64 { return &f }
func boolp(b bool) *bool      { return &b }

// bookMedia builds the deterministic media projection of a fixture book.
func bookMedia(id, title, titleIgnore, author, authorLF, narrator, series, description string, duration float64, tracks, chapters int, size int64, cover *string) mediaView {
	return mediaView{
		ID: id,
		Metadata: metadataView{
			Title: title, TitleIgnorePrefix: sp(titleIgnore), AuthorName: sp(author), AuthorNameLF: sp(authorLF),
			NarratorName: sp(narrator), SeriesName: sp(series), Genres: []string{},
			Description: descriptionOrNil(description), Explicit: boolp(false), Abridged: boolp(false),
		},
		CoverPath: cover, Tags: []string{}, Duration: f64p(duration),
		NumTracks: ip(tracks), NumAudioFiles: ip(tracks), NumChapters: ip(chapters), Size: i64p(size),
	}
}

func descriptionOrNil(description string) *string {
	if description == "" {
		return nil
	}
	return sp(description)
}

// expectedPodcast builds the podcast item with its recent episode.
func expectedPodcast(e *harness.Env, episode episodeView, progressLastUpdate int64) itemView {
	episode.LibraryItemID, episode.PodcastID = castItem, castMedia
	episode.Chapters = []any{}
	if episode.AudioFile != nil {
		episode.AudioFile.MimeType, episode.AudioFile.Codec, episode.AudioFile.Channels = "audio/mpeg", "mp3", 1
	}
	return itemView{
		ID: castItem, Ino: "1319948", LibraryID: e.IDs["podcastLibrary"], FolderID: castFolder,
		RelPath: "Fixture Cast", MtimeMs: 1793709980482, CtimeMs: 1793709980482, BirthtimeMs: 1793709979750,
		AddedAt: 1793709987709, UpdatedAt: 1793709987709, MediaType: "podcast",
		Media: mediaView{
			ID: castMedia,
			Metadata: metadataView{
				Title: "Fixture Cast", TitleIgnorePrefix: sp("Fixture Cast"), Author: sp("Fixture Host"),
				Genres: []string{"Technology"}, Explicit: boolp(false), Type: sp("episodic"),
			},
			CoverPath: sp(filepath.Join(e.Media, "podcasts", "Fixture Cast", "cover.jpg")), Tags: []string{},
			NumEpisodes: ip(3), AutoDownloadEpisodes: boolp(false), AutoDownloadSchedule: sp("0 * * * *"),
			MaxEpisodesToKeep: ip(0), MaxNewEpisodesToDownload: ip(3), Size: i64p(218107),
		},
		NumFiles: 4, Size: 219867, RecentEpisode: &episode, ProgressLastUpdate: progressLastUpdate,
	}
}

func itemsInProgressScenarios() []harness.Scenario {
	expected := func(t *testing.T, e *harness.Env) []itemView {
		return []itemView{
			expectedPodcast(e, episodeView{
				ID: e.IDs["ep3"], Season: sp("1"), Episode: sp("3"), Title: "Third Time",
				Description: sp("Episode three closes the season."), PubDate: sp("2025-03-15"),
				PublishedAt: i64p(1741996800000), AddedAt: 1793709987721, UpdatedAt: 1793709987721,
				AudioFile: &audioFileView{Duration: 12.068571},
			}, 1793709988245),
			expectedPodcast(e, episodeView{
				ID: e.IDs["ep1"], Season: sp("1"), Episode: sp("1"), Title: "Pilot Episode",
				Description: sp("The first episode of the fixture cast."), PubDate: sp("2025-03-01"),
				PublishedAt: i64p(1740787200000), AddedAt: 1793709987721, UpdatedAt: 1793709987721,
				AudioFile: &audioFileView{Duration: 24.058776},
			}, 1793709988133),
			{
				ID: e.IDs["saga2"], Ino: "836805", LibraryID: e.IDs["library"], FolderID: bookFolder,
				RelPath: "Terry Fixture/Long Saga/Book 2 - The Middle", MtimeMs: 1793709978542, CtimeMs: 1793709978542,
				BirthtimeMs: 1793709978342, AddedAt: 1793709985966, UpdatedAt: 1793709985966, MediaType: "book",
				Media: bookMedia(saga2Media, "The Middle", "Middle, The", "Terry Fixture", "Fixture, Terry", "Nora Narrator",
					"Long Saga #2", "The second book of the saga, narrated slowly.", 30, 1, 0, 218599, nil),
				NumFiles: 3, Size: 218657, ProgressLastUpdate: 1793709987379,
			},
			{
				ID: e.IDs["ebook"], Ino: "1114869", LibraryID: e.IDs["library"], FolderID: bookFolder,
				RelPath: "Ada Example/Ebook Only", MtimeMs: 1793709979610, CtimeMs: 1793709979610,
				BirthtimeMs: 1793709979594, AddedAt: 1793709985816, UpdatedAt: 1793709985816, MediaType: "book",
				Media: func() mediaView {
					media := bookMedia(ebookMedia, "Ebook Only", "Ebook Only", "Ada Example", "Example, Ada", "", "", "", 0, 0, 0, 1367, nil)
					media.Metadata.Language = sp("en")
					media.EbookFormat = sp("epub")
					return media
				}(),
				NumFiles: 1, Size: 1367, ProgressLastUpdate: 1793709987050,
			},
			{
				ID: e.IDs["saga1"], Ino: "828682", LibraryID: e.IDs["library"], FolderID: bookFolder,
				RelPath: "Terry Fixture/Long Saga/Book 1 - The Beginning", MtimeMs: 1793709978338, CtimeMs: 1793709978338,
				BirthtimeMs: 1793709977574, AddedAt: 1793709986208, UpdatedAt: 1793709986208, MediaType: "book",
				Media: bookMedia(saga1Media, "The Beginning", "Beginning, The", "Terry Fixture", "Fixture, Terry", "",
					"Long Saga #1", "", 60.186123, 3, 3, 241674,
					sp(filepath.Join(e.Media, "audiobooks", "Terry Fixture", "Long Saga", "Book 1 - The Beginning", "cover.jpg"))),
				NumFiles: 4, Size: 244064, ProgressLastUpdate: 1793709986973,
			},
		}
	}

	return []harness.Scenario{
		{
			Requirement: "route-r117", Invariant: "INV-R117-CONTRACT-001", Case: "normal", State: "stateless", Assertion: "items-in-progress-listener",
			Sources: []string{"server/routers/ApiRouter.js:191", "server/controllers/MeController.js:485-524", "server/models/LibraryItem.js:1000-1030", "go-backend/testmachine/contracts/clients/official-app.md:73"},
			Claim:   "GET /api/me/items-in-progress returns the listener's five in-progress items newest progressLastUpdate first: two podcast entries of the same item each with their recentEpisode, and the three book items (including the ebook-only one), with the minified item and media projection, while finished progress is omitted.",
			Run: func(t *testing.T, e *harness.Env) {
				var body itemsResponse
				getJSON(t, e, e.Token(t, "listener"), "/api/me/items-in-progress", &body)
				want := expected(t, e)
				if len(body.LibraryItems) != len(want) {
					t.Fatalf("want %d items, got %d", len(want), len(body.LibraryItems))
				}
				for i := range want {
					var got itemView
					body.item(t, i, &got)
					equalJSON(t, "item", got, want[i])
				}
				for i, raw := range body.LibraryItems {
					var served struct {
						Path string `json:"path"`
					}
					if err := json.Unmarshal(raw, &served); err != nil {
						t.Fatalf("item %d: %v", i, err)
					}
					folder := "audiobooks"
					if want[i].MediaType == "podcast" {
						folder = "podcasts"
					}
					if served.Path != filepath.Join(e.Media, folder, want[i].RelPath) {
						t.Fatalf("item %d path %q is not the fixture path under the install media root", i, served.Path)
					}
				}
				if want[0].RecentEpisode.Title != "Third Time" || want[1].RecentEpisode.Title != "Pilot Episode" {
					t.Fatal("podcast entries must carry their own recent episode")
				}
			},
		},
		{
			Requirement: "route-r117", Invariant: "INV-R117-LIMIT-001", Case: "edge", State: "stateless", Assertion: "items-in-progress-limit-and-empty",
			Sources: []string{"server/controllers/MeController.js:486,518-523"},
			Claim:   "GET /api/me/items-in-progress honours a numeric limit, falls back to the default for 0 and for a non-number, and returns an empty array for a user whose only progress is finished.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "listener")
				for _, limit := range []struct {
					value string
					want  int
				}{{"2", 2}, {"1", 1}} {
					if body := getItems(t, e, token, limit.value); len(body.LibraryItems) != limit.want {
						t.Fatalf("limit=%s returned %d items", limit.value, len(body.LibraryItems))
					}
				}
				if body := getItems(t, e, token, "0"); len(body.LibraryItems) != 5 {
					t.Fatalf("limit=0 must keep the default and return all 5 items: %d", len(body.LibraryItems))
				}
				if body := getItems(t, e, token, "abc"); len(body.LibraryItems) != 5 {
					t.Fatalf("a non-numeric limit must keep the default: %d", len(body.LibraryItems))
				}
				var finisher itemsResponse
				getJSON(t, e, e.Token(t, "finisher"), "/api/me/items-in-progress", &finisher)
				if finisher.LibraryItems == nil || len(finisher.LibraryItems) != 0 {
					t.Fatalf("a user without in-progress items must get an empty array: %s", finisher.LibraryItems)
				}
			},
		},
		{
			Requirement: "route-r117", Invariant: "INV-R117-CONTRACT-001", Case: "error", State: "stateless", Assertion: "items-in-progress-requires-authentication",
			Sources: []string{"server/Server.js:354", "server/routers/ApiRouter.js:191"},
			Claim:   "GET /api/me/items-in-progress answers 401 without a valid token.",
			Run: func(t *testing.T, e *harness.Env) {
				requireUnauthenticated(t, e, http.MethodGet, "/api/me/items-in-progress")
			},
		},
	}
}

// getItems performs the items-in-progress request with a limit query.
func getItems(t *testing.T, e *harness.Env, token, limit string) itemsResponse {
	t.Helper()
	resp := e.Do(t, harness.Request{Path: "/api/me/items-in-progress", Token: token, Query: map[string][]string{"limit": {limit}}})
	wantStatus(t, resp, http.StatusOK, "items in progress with limit "+limit)
	var body itemsResponse
	resp.JSON(t, &body)
	return body
}
