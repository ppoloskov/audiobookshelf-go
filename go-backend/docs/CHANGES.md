# Behaviour changes in the rewrite

For an operator upgrading from the pre-rewrite Go build (baseline commit
`7051b3e5`). The database contract and the client protocol did not change:
the same database opens in both directions, and the routes, JSON fields and
Socket.IO events that real clients use are the same. This file lists the
deliberate differences within that, one line each, as recorded during the
rewrite (`git log 7051b3e5..HEAD`).

## HTTP edge

- A request body with a wrong JSON type is a 400.
- Error bodies are plain text. A 5xx body is a fixed text; the cause is only
  logged.
- Panics and unknown paths answer plain text.
- The request pipeline is one middleware chain: recover, security headers,
  CORS, base path, clean path.
- CORS is ported from upstream, so mobile cover and ebook loads work.
- The web client fallback serves `index.html` for any extension-less GET.

## Auth

- The admin check also requires an active user.
- A stored permission flag that is not a boolean grants nothing.
- OIDC claims are read strictly.
- The OIDC user step is one transaction.

## Items

- Item views emit every key of the typed audio, library and ebook file
  documents; a stored file that lacked a key now shows it with its zero value.
- A JSON column that does not decode into its Go type reads as empty and is
  logged. It is written back unchanged until code assigns a new value.
- JSON `null` clears a metadata text field (the web client sends `null` for an
  emptied field).
- A batch update with a bad entry applies nothing.
- A file delete changes the database before the disk.
- Cover uploads are capped at 64 MiB; uploads and backup uploads at 20 GiB;
  cover and author image downloads at 10 MiB.
- Raw covers are served only from library folders and the item metadata folder.
- Custom metadata providers go through the SSRF guard.
- One deletion implementation (in `item`) serves item, library, library-issues
  and folder removal.

## Library and browse

- Filter menus are computed on demand and invalidated after writes (the old
  build kept patched in-memory copies).

## Playback

- Local-sync timestamps are numbers or numeric strings; other types read as 0.

## Sessions

- Listening-session JSON (`/api/me/listening-sessions`, listening stats, item
  sessions) carries every key of the typed session: `audioTracks` is `[]`,
  `stream`, `lastSave`, `shareSessionId`, `mediaItemShareId` and
  `coverAspectRatio` are `null`/`0` where upstream omits them, and the admin
  lists add `user` (`id`, `username`). No client reads these keys as absent.

## Podcasts

- Episode `publishedAt` is always written in the DATETIME format.
- Feeds compressed as raw deflate are not decoded.

## Scanner

- The watcher follows a moved file like a full scan.
- A bad library cron expression is logged and skipped; it no longer fails
  server start.

## Shares and feeds

- A created share always carries `isDownloadable` (upstream omits it when the
  request did not set it).
- Feed XML is written by `encoding/xml`: empty elements are an open and close
  tag pair (`<atom:link ...></atom:link>`) and a newline follows the XML
  declaration. Same elements, attributes and values.
- Otherwise the two fixes under Bugs fixed.

## Admin, settings, backup

- A settings PATCH value of the wrong type is a 400.
- An invalid backup cron expression no longer stops the server.

## Realtime

- Events are filtered on the typed principal and the typed views only.
- Online-user events carry a small struct: no token, no permissions.
- A revoked socket receives nothing.
- Playlist announcements after a deletion go through one function that `list`
  implements.

## Bugs fixed

- Series filters "finished" and "not-started" were inverted.
- Sorting by track count did not work.
- The scanner dropped the series when `metadata.json` held the string form of
  `series`; it now reads it.
- Quick match did not update `titleIgnorePrefix`; it does.
- Series routes did not check library access; they do.
- Podcast libraries now report their real issue count in the filter menus.
- An unknown feed item answered an empty 200; it is now a 404.
- A book episode `pubDate` was in local time but labelled GMT; it is now GMT.

## Removed emulation

- No fake session cookie and no other Express behaviour emulation in the auth
  flows.
- No Express error pages and no route aliases (the `//` forms).
