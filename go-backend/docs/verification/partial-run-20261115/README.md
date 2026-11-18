# Partial replacement run, 2026-11-15

Twelve of 34 packets, run directly with `go test -tags=replacement` (not the gated `make verify`; coverage is incomplete, so this is not a verdict). Base `8990c2e6` plus the uncommitted working tree. Go binary: `CGO_ENABLED=0 go build ./cmd/audiobookshelf-go`.

| Packet | Review | Scenarios | Upstream passed | Go passed | Go failed |
| --- | --- | ---: | ---: | ---: | ---: |
| admin_settings | accepted | 71 | 71 | 65 | 6 |
| auth | accepted | 100 | 100 | 92 | 8 |
| author | not accepted | 74 | 74 | 74 | 0 |
| browse | not accepted | 75 | 75 | 62 | 13 |
| collections | accepted | 62 | 62 | 38 | 24 |
| item_read | accepted | 101 | 101 | 89 | 12 |
| library_manage | accepted | 57 | 57 | 43 | 14 |
| playlists | accepted | 81 | 81 | 77 | 4 |
| server | not accepted | 4 | 4 | 4 | 0 |
| tools | not accepted | 3 | 3 | 0 | 3 |
| user_admin | accepted | 58 | 58 | 47 | 11 |
| user_me | accepted | 48 | 48 | 47 | 1 |
| **Total** | | 734 | 734 | 638 | 96 |

Scenarios of a packet share one server and run in order, so a failure can cause later failures in the same packet. Root causes are not triaged here. First output line of each Go failure (full output in [go-failures.json](go-failures.json)):

## admin_settings

- `validate-cron-valid-expressions`: cron.go:24: expression */5 * * * * *: want status 200, got 400: Invalid cron expression
- `settings-patch-invalid-body-rejected`: settings.go:53: body null: want status 400, got 200: {"serverSettings":{"id":"server-settings","scannerFindCovers":false,"scannerCoverProvider":"google","scannerParseSubtitle":false,"scannerPreferMatchedMetadata":false,"
- `settings-backup-schedule-starts-and-stops-backups`: settings.go:223: timed out waiting for scheduled backup
- `sorting-prefixes-restore-and-drop-non-strings`: prefixes.go:132: sorting-prefixes: want status 200, got 400: Invalid request body
- `auth-patch-invalid-values-ignored`: authsettings.go:113: want 200, got 400: Invalid value for "authActiveAuthMethods"
- `auth-incomplete-openid-dropped-at-restart`: lifecycle.go:74: stored methods = [local]

## auth

- `malformed-login-json-rejected`: login_scenarios.go:21: login input {"username": must return 400 without token material; got 401
- `missing-login-credential-keys-rejected`: login_scenarios.go:29: login input {"password":"trial-listener-password"} must return 400 without token material; got 401
- `local-auth-disabled-login-denied`: login_checks.go:104: login with local auth disabled: status 200: {"user":{"id":"f6af59cf-3988-45fe-b0ad-81420a953f9d","username":"trial-listener","email":null,"type":"user","token":"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.e
- `authorize-rejects-expired-api-key-and-deactivates-it`: authorize.go:192: timed out waiting for the expired key to be deactivated
- `password-change-with-refresh-token-keeps-current-session`: lifecycle.go:230: old token of the current session: want 401 with an error message and no tokens, got 200: {"user":{"id":"f45ded7e-e003-4d62-9727-b025f48176e0","username":"auth-scenario-42","email":null,"type":"user","to
- `root-token-hidden-from-admins`: users_acl.go:87: root token hidden = false (listed true), want true
- `malformed-permission-input-is-ignored`: users_acl.go:199: create: status 400, want 200: Invalid request
- `api-key-root-ownership-and-lifecycle`: users_acl.go:379: a no-op patch must not touch updatedAt

## browse

- `book-items-sort-keys`: items_book.go:189: sort media.metadata.authorName desc=: got [novel ebook saga2 saga1 disposable], want [ebook novel saga1 saga2 disposable]
- `book-items-sort-ties-random-and-unknown`: items_book.go:209: GET items map[sort:[nonsense]]: status 400: unsupported sort field
- `book-items-filter-groups`: items_filters.go:108: filter genres.Fiction: got [], want [novel]
- `book-items-filter-progress-per-user`: items_filters.go:135: finisher finished: got [disposable novel saga2 saga1], want [saga1]
- `book-items-filter-unknown-and-series-edges`: items_filters.go:149: GET items map[filter:[bogus] sort:[media.metadata.title]]: status 501: unsupported filter group
- `book-items-series-filter-sequence-order`: items_filters.go:165: default: got [saga2 saga1], want [saga1 saga2]
- `podcast-items-filters`: items_more.go:226: podcast filter genres.Technology: got [], want [Fixture Cast]
- `book-items-tag-restriction`: items_more.go:269: tagged beta filter: got [], want [novel]
- `series-list-sort-keys`: series_list.go:74: series sort limit=10&sort=lastBookUpdated: got [Long Saga Solo Series A Third Saga], want [Solo Series Long Saga A Third Saga]
- `series-list-filter-groups`: series_list.go:149: series filter genres.Fantasy: got [], want [Long Saga]
- `personalized-book-shelves-finisher-and-root`: personalized.go:123: finisher shelves: got [continue-series/book/saga2/1 recently-added/book/saga1,saga2,novel/3 recent-series/series/A Third Saga,Solo Series,Long Saga/3 discover/book/disposable,novel/2 listen-again/boo
- `search-invalid-query-and-access`: search.go:191: search q=a&q=b: status 200, want 400: {"book":[{"libraryItem":{"id":"da2e4ecd-d9cc-44be-bac7-6b30992d1c0e","ino":"1270531","oldLibraryItemId":null,"libraryId":"51bf3bdf-f246-4f7d-b0a6-9059e4d0ed41","folder
- `series-patch-visible-in-reads`: series_patch.go:93: item seriesName "The Long Saga #2"

## collections

- `library-list-rejections`: read.go:209: limit=1.5: status 200, want 400
- `create-optional-description-and-falsy-books`: create.go:79: status = 400, want 200; body Invalid collection data. Invalid books
- `create-invalid-bodies-rejected-unchanged`: create.go:140: duplicate book: status 500, want 400: Internal Server Error
- `patch-repeat-and-empty-change-keep-row`: update.go:82: status = 400, want 200; body Invalid value for "name"
- `patch-null-description-clears-and-partial-keeps`: update.go:106: null description must clear it: {ID:7eeebd3b-cd79-4228-9449-4a062ef63729 LibraryID:51bf3bdf-f246-4f7d-b0a6-9059e4d0ed41 Name:Keep name Description:0x2d48357167d0 Books:[{ID:b3c61dfc-dd4b-47ee-8998-0340c122
- `remove-book-by-user-with-delete-permission`: books.go:147: status = 403, want 200; body Forbidden
- `batch-add-skips-members-unknown-and-non-strings`: books.go:194: status = 400, want 200; body Invalid value for "books.1"
- `batch-remove-repeat-and-nonmembers-noop`: books.go:268: status = 400, want 200; body Invalid value for "books.2"
- `batch-remove-rejected-unchanged`: books.go:303: status = 400, want 500; body Invalid request body
- `event-added-reaches-every-authenticated-socket`: events.go:34: no "collection_added" event within 5s; events since 1: []
- `event-added-repeat-emits-new-collection`: events.go:59: no "collection_added" event within 5s; events since 1: []
- `event-added-by-updater-user`: events.go:73: no "collection_added" event within 5s; events since 1: []
- `event-removed-reaches-every-authenticated-socket`: events.go:102: no "collection_removed" event within 5s; events since 1: []
- `event-removed-emptied-collection-has-no-books`: events.go:118: no "collection_removed" event within 5s; events since 1: []
- `event-removed-not-repeated-for-second-delete`: events.go:133: no "collection_removed" event within 5s; events since 1: []
- `event-updated-by-patch`: events.go:165: no "collection_updated" event within 5s; events since 1: []
- `event-updated-at-every-call-site`: events.go:199: no "collection_updated" event within 5s; events since 1: []
- `event-updated-not-sent-for-rejected-requests`: events.go:243: status = 400, want 500; body Invalid request body
- `get-hides-restricted-books`: restricted.go:35: status = 200, want 404; body {"id":"6771728d-25b2-483d-88a9-a46171160c6c","libraryId":"51bf3bdf-f246-4f7d-b0a6-9059e4d0ed41","name":"Explicit only","description":null,"books":[],"lastUpdate":17947118857
- `list-hides-collection-with-only-restricted-books`: restricted.go:57: finisher list wrong: 41 collections
- `library-list-hides-restricted-collection`: restricted.go:75: restricted collection must be hidden: before 42 after 42
- `get-tag-restricted-user`: paging.go:100: status = 200, want 404; body {"id":"4074b471-7eca-4e11-8ea0-2497e9e690e7","libraryId":"51bf3bdf-f246-4f7d-b0a6-9059e4d0ed41","name":"Tag C","description":null,"books":[],"lastUpdate":1794711886172,"created
- `list-tag-restricted-user`: paging.go:127: allow-mode list wrong
- `library-list-tag-restricted-user`: paging.go:148: allow-mode library list wrong, total 53

## item_read

- `metadata-object-non-admin-forbidden`: tools.go:100: non-admin unknown item: status 403, want 404: Forbidden
- `ffprobe-non-admin-forbidden`: tools.go:216: unknown file: status 403, want 404: Forbidden
- `file-unknown-file-id`: files.go:155: inaccessible item, unknown file: status 404, want 403: Not Found
- `file-item-access-denied`: access.go:85: tagallow: item without tags under a tag allow list: status 200, want 403: ID3??????vTIT2???????Part 1?TPE1???????Terry Fixture?TALB???????The Beginning?TRCK???????1/3?TSSE???????Lavf60.16.100?????????????p?
- `file-download-apple-mobile-m4b`: files.go:206: Single File Novel.m4b: Content-Type "audio/mp4", want "audio/m4b"
- `file-download-item-access-denied`: access.go:85: tagallow: item without tags under a tag allow list: status 200, want 403: ID3??????vTIT2???????Part 1?TPE1???????Terry Fixture?TALB???????The Beginning?TRCK???????1/3?TSSE???????Lavf60.16.100?????????????p?
- `ebook-missing-or-invalid`: ebook.go:58: unknown file id: status 400, want 404: Invalid ebook file id
- `ebook-item-access-denied`: access.go:85: tagallow: item without tags under a tag allow list: status 404, want 403: Not Found
- `download-item-access-denied`: access.go:85: tagallow: item without tags under a tag allow list: status 200, want 403: PK????????/?o]????????????????01 - Part 1.mp3UT????!?jID3??????vTIT2???????Part 1?TPE1???????Terry Fixture?TALB???????The Beginning?
- `file-x-accel-redirect`: xaccel.go:23: X-Accel route: status 200, want 204: ID3??????vTIT2???????Part 1?TPE1???????Terry Fixture?TALB???????The Beginning?TRCK???????1/3?TSSE???????Lavf60.16.100?????????????p??????????Info??????????:.????????????
- `file-download-x-accel-redirect`: xaccel.go:23: X-Accel route: status 200, want 204: ID3??????vTIT2???????Part 1?TPE1???????Terry Fixture?TALB???????The Beginning?TRCK???????1/3?TSSE???????Lavf60.16.100?????????????p??????????Info??????????:.????????????
- `ebook-x-accel-redirect`: xaccel.go:23: X-Accel route: status 200, want 204: PK??

## library_manage

- `list-include-stats`: list.go:103: library 51bf3bdf-f246-4f7d-b0a6-9059e4d0ed41 stats: {"libraries":[{"id":"51bf3bdf-f246-4f7d-b0a6-9059e4d0ed41","name":"Audiobooks","folders":[{"id":"bc88289a-fc01-4256-a39e-90649091e152","fullPath":"/private
- `create-podcast-explicit-fields-and-settings`: create.go:65: setup: create library returned 400 Invalid value for ""
- `create-book-settings-variants`: create.go:106: an empty cron expression must leave the default: map[audiobooksOnly:false autoScanCronExpression: coverAspectRatio:1 disableWatcher:false epubsAllowScriptedContent:false hideSingleBookSeries:false markAsFi
- `create-with-existing-custom-provider`: create.go:189: setup: create library returned 400 Invalid request. Custom metadata provider does not exist
- `create-invalid-bodies-rejected-without-effects`: create.go:255: percent above 100: status 200, want 400 ({"id":"336379d9-1952-4a14-b476-8cb7ca2881ca","name":"x","folders":[{"id":"5b59bd62-31df-4815-af1c-49f6cccbb599","fullPath":"/private/var/folders/jt/2prvbq7965ggn35r
- `update-settings-merge-over-previous-state`: update.go:65: patch library: status 400, want 200 (Invalid value for "")
- `update-invalid-bodies-rejected-without-effects`: update.go:247: missing custom provider: status 200, want 400 ({"id":"7656241d-e842-4cc7-9ce7-754485ae3c5e","name":"invalid-48","folders":[{"id":"bbed40d4-a6b0-4002-b377-f07746dfccb9","fullPath":"/private/var/folders/jt/2
- `order-invalid-bodies-rejected-without-effects`: order.go:165: missing newOrder: status 200, want 400 ({"libraries":[{"id":"d748b955-8fdd-4d77-a008-7bf74c1a7762","name":"partial-78","folders":[{"id":"4015b95e-3bd1-43f5-bb1d-7856d66b41cc","fullPath":"/private/var/folder
- `event-updated-audience-and-payload`: events.go:29: unexpected "library_updated" event: [{"id":"9043f9d6-65a8-46d0-a01b-ba99ac48263b","name":"Event Renamed","folders":[{"id":"bca5890b-e38f-4ac8-9c79-db6331311752","fullPath":"/private/var/folders/jt/2prvbq796
- `event-updated-for-folder-change-and-granted-user`: events.go:51: unexpected "library_updated" event: [{"id":"812e96c9-2a87-4ea4-af81-4c73be12fbc2","name":"ev-folder-89","folders":[{"id":"c6c2a60c-48cd-41b8-ad29-224f68d0e330","fullPath":"/private/var/folders/jt/2prvbq7965
- `event-added-audience-and-payload`: events.go:98: unexpected "library_added" event: [{"id":"6cc34594-7c57-48bf-bf21-f93d4ecf27fc","name":"ev-add-101","folders":[{"id":"4ec7f78f-110b-4e1d-909e-49c9e84beb58","fullPath":"/private/var/folders/jt/2prvbq7965ggn3
- `event-added-follows-user-access`: events.go:126: unexpected "library_added" event: [{"id":"069afc54-f1e4-4948-a31f-f0eb5d8f28cc","name":"ev-later-a-106","folders":[{"id":"a5937268-c2c6-4e66-b376-057c3e912580","fullPath":"/private/var/folders/jt/2prvbq796
- `update-lookup-precedes-admin-check-precedes-validation`: checkorder.go:42: unknown id: status 403, want 404 (Forbidden)
- `delete-lookup-precedes-admin-check`: checkorder.go:53: unknown id: status 403, want 404 (Forbidden)

## playlists

- `library-list-rejections`: read.go:261: limit=1.5: status 200, want 400
- `patch-same-values-and-empty-body-change-nothing`: update.go:93: body map[description:] changed the playlist: {ID:85ddf3c5-2a9b-4aa8-ad9b-8b63a2607448 Name:Stable LibraryID:51bf3bdf-f246-4f7d-b0a6-9059e4d0ed41 UserID:89e07f47-48e5-4981-a849-04fa7472fb9f Description:<nil>
- `from-collection-hides-restricted-books`: restricted.go:76: pl-tag-allow playlist items = [7baea19e-3334-416f-b862-0cb7499e5006], want [b3c61dfc-dd4b-47ee-8998-0340c1228f52 7baea19e-3334-416f-b862-0cb7499e5006]
- `from-collection-404-when-every-book-hidden`: restricted.go:96: status = 400, want 404; body Collection has no books

## tools

- `immediate-embed-has-no-queued-transition`: packet.go:24: root embed trace = [queue:b3c61dfc-dd4b-47ee-8998-0340c1228f52:true queue:b3c61dfc-dd4b-47ee-8998-0340c1228f52:false task_started:b3c61dfc-dd4b-47ee-8998-0340c1228f52 task_finished:b3c61dfc-dd4b-47ee-8998-0
- `waiting-embed-admission-and-fifo-dequeue-audiences`: packet.go:31: root embed trace = [queue:b3c61dfc-dd4b-47ee-8998-0340c1228f52:true queue:91bb63f7-97c9-4230-8fd0-1dfd994977ea:true queue:7baea19e-3334-416f-b862-0cb7499e5006:true queue:b3c61dfc-dd4b-47ee-8998-0340c1228f52
- `idle-queue-repeat-keeps-immediate-and-waiting-distinct`: packet.go:38: listener embed trace = [queue:b3c61dfc-dd4b-47ee-8998-0340c1228f52:false task_started:b3c61dfc-dd4b-47ee-8998-0340c1228f52 task_finished:b3c61dfc-dd4b-47ee-8998-0340c1228f52 queue:91bb63f7-97c9-4230-8fd0-1d

## user_admin

- `list-users-include-latest-session`: list.go:140: include "latestSession": the listener's latest session must be the newest updated one: <nil>
- `get-user-root-token-and-empty-lists`: get.go:83: an admin must not see the root token: {ID:c2eedd44-2b7f-44cf-a18f-2687b798c386 Username:trial-root Email:<nil> Type:root Token:eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJ1c2VySWQiOiJjMmVlZGQ0NC0yYjdmLTQ0Y2YtYTE4Z
- `update-user-lists-only-inside-permissions`: update.go:102: a rejected request must not change the users row:
- `update-user-ignores-invalid-values`: update.go:119: PATCH /api/users/3ac2ddaa-fdc9-4b60-a7aa-915c86313a86: want 200, got 400 Invalid request
- `update-user-repeat-changes-nothing`: update.go:147: a rejected request must not change the users row:
- `update-user-username-rotates-token`: update.go:216: a rejected request must not change the users row:
- `user-updated-event-per-change`: update.go:431: unexpected "user_updated" event: [{"id":"abcd002f-b18a-47ad-9881-f4069ad254c2","username":"event-twice","email":"two@example.com","type":"user","token":"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpYXQiOjE3OTQ
- `user-updated-event-not-sent-without-change`: update.go:446: unexpected "user_updated" event: [{"id":"a6a4996a-4bc2-4ff4-ab97-987beb33e0d3","username":"event-nochange","email":null,"type":"user","token":"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpYXQiOjE3OTQ3MTE4NzAsI
- `unlink-clears-subject-and-emits`: unlink.go:63: extraData must keep the other keys and null the subject: map[seriesHideFromContinueListening:[]]
- `unlink-twice-and-unlinked-user`: unlink.go:91: unlink without a link: want 200, got 403 Forbidden
- `user-removed-event-for-user-with-data`: delete.go:260: payload must carry the progress and bookmarks: {ID:ce0885da-7590-4172-b4fc-9ce418acb954 Username:del-event-data Email:<nil> Type:user Token:eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJpYXQiOjE3OTQ3MTE4NzMsInVz

## user_me

- `remove-continue-repeat-is-quiet`: continue.go:166: browser view unchanged by repeat:

