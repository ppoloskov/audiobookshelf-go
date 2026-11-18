# Raw client evidence (input for MC-A)

Per-client extraction of every server operation found in the six clients'
source at the SHAs in `clients.json` (full clones, 2026-11-04). Each file has
an HTTP table with file:line citations plus Socket.IO, auth, playback,
download/range, caching and version-branch sections, each item marked
CONFIRMED / INFERRED / NOT FOUND.

This is input for the census task MC-A in `../mobile-client-audit.md`, not a
second audit. `tools/clientaudit/build.py` turns it into SHA-pinned
client x operation rows matched to upstream routes and Go handlers (prints
Markdown to stdout; edits nothing).
