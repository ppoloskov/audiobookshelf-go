// Package version names the upstream Audiobookshelf release this server is
// compatible with. It is the one place that string is written.
package version

// Monstrobook is the release version of this Go server implementation.
const Monstrobook = "0.1.0"

// Server is the upstream Audiobookshelf version reported to clients, stored in
// lastScanVersion, checked against migrationsMeta, and written into backups and
// sessions.
const Server = "2.37.2"
