// Package db owns every persisted shape and the data-access handle.
//
// DB (db.go) wraps the SQLite handle with typed row scanning, IN-list
// expansion and context-carried transactions. open.go opens, validates and
// creates the database file. types.go has the column types (Time, JSON, Bool),
// models.go one struct per table and documents.go one struct per JSON-column
// document. schema.sql is the pinned TS v2.37.2 schema. The dbtest subpackage
// opens test databases.
package db
