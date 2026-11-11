// Package db owns every persisted shape and the data-access handle.
//
// DB (db.go) wraps the SQLite handle with typed row scanning, IN-list
// expansion and context-carried transactions. types.go has the column types
// (Time, JSON, Bool), models.go one struct per table and documents.go one
// struct per JSON-column document. The dbtest subpackage opens test databases.
//
// The directory also holds the SQL source for typed queries used by older
// packages: schema.sql (the pinned TS v2.37.2 schema), queries/ (one file per
// domain) and sqlc.yaml. The Go code sqlc generates lives in internal/db/sqlc;
// run `make sqlc` to rebuild it.
package db
