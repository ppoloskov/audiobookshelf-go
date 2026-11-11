Scope: non-test code is about 4,380 lines: podcasts 2,155, providers 1,962, textclean 236, textdist 31. Tests are about 2,900 lines.

## 1. RESPONSIBILITIES
- podcasts/podcasts.go (356): the Handler type, routes, the onItem permission middleware, feed/OPML-parse/downloadEpisodes/searchEpisode/checkNew/matchEpisodes handlers, plus coercion helpers (`stringLike`, `stringArray`) and an unused `FetchFeedEpisodes`.
- podcasts/create.go (284): podcast creation from a `map[string]any` body, the OPML import goroutine, and the task records for it.
- podcasts/rss.go (365): gofeed-based feed parsing, API-shaped Feed/Episode structs, OPML parse, feed fetching (gzip/deflate, 32MB cap, User-Agent).
- podcasts/downloads.go (310): serial download queue, file fetch, ffprobe, `episodeFilename`.
- podcasts/episode_registration.go (226): INSERT of the episode row, `libraryFiles` rewrite, notifications, trimming to maxEpisodesToKeep.
- podcasts/episode_mutations.go (190): PATCH and DELETE of an episode, `deleteEpisode`.
- podcasts/schedule.go (147): cron entries, `runScheduled`, failure counting, `filterNew`.
- podcasts/quickmatch.go (129): match local episodes without an enclosure to feed episodes by Levenshtein similarity (threshold 0.9).
- podcasts/library.go (132): recent-episodes and episode-downloads queries for a library.
- podcasts/dto.go (16): two response structs.
- providers/handler.go (293): custom-provider CRUD, the provider catalog, an endpoint map (for test fixtures), small helpers.
- providers/builtin_search.go (740): the `/api/search/*` handlers and 7 clients: Google, iTunes, OpenLibrary, AudiobookCovers, FantLab, Audible/Audnexus, and `getJSON`.
- providers/book_finder.go (419): BookFinder port (fuzzy title/author candidates, OpenLibrary distance filter, Audible duration sort).
- providers/match.go (170): lenient JSON types and `bookMatch`.
- providers/cover_search.go (147): socket-driven concurrent cover search.
- providers/author_match.go (92): Audnexus author lookup.
- providers/custom_search.go (80): the custom-provider HTTP adapter.
- providers/podcast_finder.go (21): iTunes podcast search wrapper.
- textclean (236): `Sanitize` and `Strip`, built on the x/net/html tokenizer.
- textdist (31): Levenshtein.

Routes:
- Podcasts, admin only: POST /api/podcasts, /feed, /opml/parse, /opml/create.
- Podcasts, per item (library access, then update/delete permission by HTTP method):
  - GET /api/podcasts/{id}/downloads, /clear-queue, /search-episode, /checknew, /episode/{eid}
  - POST /api/podcasts/{id}/download-episodes, /match-episodes
  - PATCH and DELETE /api/podcasts/{id}/episode/{eid}
- Libraries: GET /api/libraries/{id}/recent-episodes and /episode-downloads.
- Search: GET /api/search/providers, /books, /covers, /podcast, /authors, /chapters.
- Custom providers, admin only: GET and POST /api/custom-metadata-providers, DELETE /api/custom-metadata-providers/{id}.
- Socket: `search_covers` and `cancel_cover_search` are wired in server/runtime.go:203-206. Events emitted: `cover_search_result`, `cover_search_provider_error`, `cover_search_complete`, `cover_search_cancelled`.

## 2. TABLES / EXTERNAL
- Reads: libraryItems, libraryFolders, libraries, podcasts, podcastEpisodes, mediaProgresses, customMetadataProviders, plus serversettings and the `items` loader.
- Writes:
  - podcasts: INSERT, `lastEpisodeCheck`, `autoDownloadEpisodes`, `numEpisodes`.
  - libraryItems: INSERT, `libraryFiles`, `size`.
  - podcastEpisodes: INSERT, UPDATE, DELETE (via `store.DeleteEpisodes`).
  - customMetadataProviders: INSERT, DELETE.
  - libraries: `provider` fallback when a custom provider is deleted.
- External HTTP:
  - Arbitrary RSS and enclosure URLs, through `httputil.SafeClient` (the SSRF guard).
  - iTunes, Google Books, OpenLibrary + covers, FantLab, Audible catalog, Audnexus (books, authors, chapters), AudiobookCovers, and user-defined custom providers.
- providers uses a plain `http.Client` with a 10s timeout, not SafeClient. Custom-provider URLs are admin-set, but there is no SSRF guard.
- Processes: ffprobe (via `mediatools`).

## 3. COUPLING
- `podcasts.New(db, cfg, emit, notify)`:
  - `notify` is `NotificationFunc` (`notifications.TriggerEvent`), passed as a callback to avoid a package import.
  - `emit` is `events.Emitter`.
  - Direct imports include access, items, `podcasts/read`, `store`, `mediafiles`, `mediatools`, `playlistview`, `serversettings`, `cronspec`, `wire`.
- Exported to catalog and server:
  - `QuickMatchEpisodes` and `Reschedule` are injected into catalog Deps (`PodcastEpisodes`, `PodcastScheduleChanged`) at runtime.go:111.
  - `EpisodeDownloads` satisfies the catalog `Downloads` interface (catalog/handler.go:20).
- `providers.Handler` satisfies:
  - catalog `Books` (`SearchBooks`, `SearchPodcasts`; quickmatch.go:32), which returns `wire.BookMatch` / `wire.PodcastMatch` with every field `json.RawMessage`.
  - people `Authors` (`FindAuthorByASIN`, `FindAuthorByName`).
  - the realtime socket, via `reply func(event, payload)` passed to `StartCoverSearch`.
- textclean is imported by identity, adminops, catalog, books, podcasts and providers. `wire` types exist only so these packages avoid importing each other.
- `podcasts.Handler` has no other callers; `FetchFeedEpisodes` is dead.

## 4. TESTS
- podcasts: about 1,900 lines.
  - Black-box (about 55%): `rss_test.go` and `lifecycle_test.go` drive Routes and handlers against httptest feed/episode servers and a real DB.
  - White-box (about 45%): they call unexported `parseFeed`, `parseOPML`, `filterNew`, `matchEpisodes`, `registerEpisode`, `enqueue` and `runScheduled`, and swap `h.emit`.
- providers: about 590 lines.
  - Roughly 65% is HTTP-level: `SetEndpoint` fixtures plus a mux.
  - The rest is white-box: `cleanTitleForCompares`, `titleCandidates`.
- textclean: 349 lines, golden tests, a pure function API. It stays valid after a rewrite, and one test compares against upstream sanitize-html output.
- textdist: 40 lines.
- Most survive a rewrite if kept at the HTTP and fixture level. About 40% bind to internals.

## 5. COMPLEXITY SOURCES
1. create.go:36-179 (~145 lines): `createPodcastFromBody` takes `map[string]any`, with a (status, message, err) return and JS-style coercion (`stringLike`, `anyString`, `metadataValue`, podcasts.go:316-356). OPML import builds a map payload and feeds it back through it (create.go:228-245).
2. providers/builtin_search.go:52-740 (~690 lines): seven providers inline, with duplicated URL/decode/normalize code and a lenient `getJSON` (452-484, ignores type mismatches).
3. providers/match.go (170) plus wire/match.go: `text`/`num`/`list`/`year` lenient types, then `convertJSON` (book_finder.go:79-92) round-trips typed results to RawMessage per result.
4. book_finder.go:100-419 (~320 lines): BookFinder port.
   - Includes a hand-rolled regex lookbehind `removeMiddleInitials` (~45 lines, book_finder.go:140-185) and 7 regex transformers.
   - The title-candidate ordering is stateful.
5. episode_mutations.go:19-125 (~105 lines): PATCH through `map[string]any`, a dynamic SET builder, and chapter validation via JS-like checks (`chapter["start"] == float64(0)`).
6. episode_registration.go:23-165 (~140 lines): `registerEpisode` as one big function (stat_t ino, `mediafiles.File` as a map, `libraryFiles` JSON rewrite). `trimOldestEpisode` adds 45 (180-226).
7. quickmatch.go:14-110 (~95 lines): 11 `sql.NullString` fields scanned by hand, a dynamic UPDATE, and a title similarity of `0.7*title + 0.3*subtitle` (`matchEpisodes`) next to a second variant (`bestFeedEpisode`).
8. downloads.go:32-310 (~280 lines): hand-rolled serial queue, mutex, current/queue, `downloadTask` with ctx/cancel and `clientJSON`. Together with schedule.go (147), it keeps state in `failedCheckMap` and `scheduleIDs` under one `queueMu`.
9. textclean.go (236): hand-written sanitizer replicating sanitize-html quirks (p/li auto-close, `<br />`, iframe/noscript renaming, obfuscated-href detection). Bluemonday is not in go.mod.
10. rss.go:330-365 `decodedBody` (~35): manual gzip/deflate/zlib sniffing, needed only because `Accept-Encoding` is set by hand. `parseOPML` (rss.go:235-270, ~35) is lenient XML, case-insensitive. `numText` and `chapter` types add more.

Other notes: cover_search.go (147) runs a goroutine per provider (all = 15). handler.go:55-130 decodes custom-provider creation from `map[string]any`.

## 6. CLIENT DEPENDENCE
- `descriptionPlain` is read in BookMatchCard.vue:35 and Match.vue:99, and `feedMetadata.descriptionPlain` in NewModal.vue:224. Real dependence.
- Series entries `{series, sequence}` and `publishedYear` as string/number are read in Match.vue. Plausible.
- Socket `cover_search_*` and `episode_download_*` events are consumed (Cover.vue:320; download-queue.vue:125). Real.
- Client `v-html` use (21 sites) means sanitized HTML is rendered, so the allowlist matters.
- The JS-style leniency (number to string, comma-joined list, `{value:}` objects) exists to tolerate custom-provider output, not the web client. Unknown for mobile.

## 7. GREENFIELD PROPOSAL
- `internal/metadata`:
  - `Provider` interface: `SearchBooks(ctx, Query) ([]BookResult, error)`, plus `PodcastProvider` and `CoverProvider` options.
  - One `BookResult` struct with json tags (Title, Author, Series []Series, PublishedYear string, Genres []string…), one `PodcastResult`.
  - One file per provider: google.go, itunes.go, openlibrary.go, fantlab.go, audible.go (Audible + Audnexus), custom.go. Shared `fetchJSON`.
  - `finder.go`: the fuzzy logic, reduced to title/author candidate generation.
  - `covers.go`: a registry with a `Cover(ctx)` method on each provider.
  - `customproviders.go`: CRUD with a typed request struct.
- Lenient decoding stays only at the custom-provider edge (one `UnmarshalJSON` on `BookResult`). Built-ins decode into private structs and return typed values, which removes `convertJSON` and the `wire.BookMatch` RawMessage layer (catalog gets typed results).
- `internal/podcasts` splits into:
  - `feed` (gofeed to `Feed`/`Episode`; `Accept-Encoding` left to net/http)
  - `downloader` (a goroutine consuming a channel, one worker, `Events` callbacks)
  - `schedule`
  - `episodes` (a store with typed `Update` structs, replacing the maps)
  - `handlers.go`
- Libraries:
  - bluemonday with a custom policy (about 10 lines) for textclean, if golden tests agree.
  - `agnivade/levenshtein` for textdist, if a dependency is acceptable.
  - `encoding/xml` struct for OPML.
  - `golang.org/x/net/html` stays for Strip.
- Estimated size: about 2,200 lines after against 4,380 now (podcasts ~1,300, providers ~800 excluding the fuzzy finder at ~250, textclean ~40 with bluemonday).

## 8. HAZARDS
- Persisted episode columns:
  - `index` is written NULL.
  - `enclosureSize` is an int or NULL.
  - `extraData` is `{"guid":...}`.
  - `chapters` is `[]`.
  - `audioFile` is JSON with `ino`, `metadata` and `timeBase`.
  - `publishedAt` is RFC3339Nano in registration but `dbtime.Format` in quickmatch. Check which one upstream uses.
  - `libraryFiles` gains an entry with `fileType:"audio"` and `isSupplementary:null`; `libraryItems.size` is summed; `numEpisodes` is recounted.
- Duplicate detection by guid or enclosure URL, using `known["g:"+guid]` or `known["u:"+url]` (schedule.go:128-139). Cutoff is `publishedAt > max(lastEpisodeCheck, newest episode)`. The manual `checknew` uses a limit of 3.
- Download queue:
  - Serial; dedupe by item and URL.
  - The first download is not announced as `episode_download_queued`, only later ones.
  - Events: `started`, `finished`, `queue_cleared`.
  - Task type `download-podcast-episode` with data `{libraryId, libraryItemId}`, titles and string keys.
  - On a filename collision the file gets ` (taskid)` appended, and `appendRandomId` is reported.
  - Temp file, then rename.
- Auto-download:
  - Cron-based, loaded at start; `Reschedule` is called after a podcast update.
  - `failedCheckMap` is in memory and triggers `onRSSFeedFailed` / `onRSSFeedDisabled`.
  - `maxFailedChecks` disables auto-download.
  - Trimming to `maxEpisodesToKeep` and notifications happen only for auto-downloaded episodes.
  - `lastEpisodeCheck` is updated on every check.
- Sanitized description HTML, allowlist `p ol ul li a strong em del br b i`; `<br />`, `href` only http/https/mailto/relative; `descriptionPlain` via Strip. Applied to feed, iTunes, custom-provider and PATCH episode description/subtitle.
- Custom provider protocol: `GET {url}/search?mediaType&query[&author][&isbn]`, `Authorization` header set verbatim, 10s timeout, response `{matches:[]}`. Remote failure returns empty; malformed `matches` is an error. Deleting a provider resets `libraries.provider` to google or itunes. Slug is `custom-{id}`.
- Error and edge semantics: `onItem` returns 500 (not 404) for a non-podcast item; `checknew` bad feed returns `{"episodes":[]}` while `search-episode` returns `{"episodes":null}`; `/api/search/chapters` returns 200 with an error body.
