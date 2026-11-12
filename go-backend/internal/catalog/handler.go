// Package catalog implements catalog API endpoints for the pinned
// Audiobookshelf SQLite schema.
package catalog

import (
	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/item"
	"context"
	"database/sql"
)

// Deps are the collaborators catalog routes use outside this package. They are
// interfaces and functions defined here and satisfied by feature packages,
// because feature packages do not import each other; the composition root
// supplies them to New. Filters is required; every other field is optional.
type Deps struct {
	// Filters is the library filter data cache shared with the scanner.
	Filters *filterdata.Cache
	// LibraryChanged refreshes the scanner's watcher and schedules after a
	// library mutation committed.
	LibraryChanged func(libraryID string)
	// QuickMatch quick matches one library item for the library match-all task.
	QuickMatch func(ctx context.Context, itemID string, opts item.MatchOptions) (item.MatchResult, error)
}

type Handler struct {
	db      *sql.DB
	cfg     config.Config
	emit    events.Emitter
	filters *filterdata.Cache
	deps    Deps
}

// New's emit callback sends events from catalog write routes.
func New(db *sql.DB, cfg config.Config, emit events.Emitter, deps Deps) *Handler {
	return &Handler{db: db, cfg: cfg, emit: emit, filters: deps.Filters, deps: deps}
}
