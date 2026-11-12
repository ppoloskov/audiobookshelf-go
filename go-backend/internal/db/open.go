package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"audiobookshelf-compat/internal/version"

	sqlite "modernc.org/sqlite" // the database/sql driver
)

// ErrSchema marks a database file this server refuses to run on: another
// migration state, or a model column missing from a table.
var ErrSchema = errors.New("db: unsupported or incomplete Audiobookshelf schema")

// FilePath is where the database lives inside the config directory.
func FilePath(configDir string) string { return filepath.Join(configDir, "absdatabase.sqlite") }

// tableRows maps every table of the pinned schema to its row struct. Validate
// compares the two.
var tableRows = []struct {
	table string
	row   any
}{
	{"apiKeys", ApiKey{}}, {"authors", Author{}}, {"bookAuthors", BookAuthor{}},
	{"bookSeries", BookSeries{}}, {"books", Book{}}, {"collectionBooks", CollectionBook{}},
	{"collections", Collection{}}, {"customMetadataProviders", CustomMetadataProvider{}},
	{"devices", Device{}}, {"feedEpisodes", FeedEpisode{}}, {"feeds", Feed{}},
	{"libraries", Library{}}, {"libraryFolders", LibraryFolder{}}, {"libraryItems", LibraryItem{}},
	{"mediaItemShares", MediaItemShare{}}, {"mediaProgresses", MediaProgress{}},
	{"migrationsMeta", MigrationsMeta{}}, {"playbackSessions", PlaybackSession{}},
	{"playlistMediaItems", PlaylistMediaItem{}}, {"playlists", Playlist{}},
	{"podcastEpisodes", PodcastEpisode{}}, {"podcasts", Podcast{}}, {"series", Series{}},
	{"sessions", Session{}}, {"settings", Setting{}}, {"users", User{}},
}

// Connect opens an existing database file with the one set of connection
// settings (a 5 s busy timeout, foreign keys on, a single connection) and
// checks that foreign keys are on. It does not validate the content; Open
// does. No journal-mode pragma is set: the file stays in rollback-journal mode
// so upstream can reopen it.
func Connect(path string) (*DB, error) { return connect(path, nil) }

// ConnectWrapped is Connect with every driver connection passed through wrap,
// so a test can count or fail queries. It is for tests only; production code
// uses Connect or Open.
func ConnectWrapped(path string, wrap func(driver.Conn) driver.Conn) (*DB, error) {
	return connect(path, wrap)
}

func connect(path string, wrap func(driver.Conn) driver.Conn) (*DB, error) {
	if path == "" {
		return nil, errors.New("db: database path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("db: resolve database path: %w", err)
	}
	if _, err := os.Stat(abs); err != nil {
		return nil, fmt.Errorf("db: database file %q: %w", abs, err)
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := u.Query()
	q.Set("mode", "rw")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	u.RawQuery = q.Encode()
	var sqlDB *sql.DB
	if wrap == nil {
		var err error
		if sqlDB, err = sql.Open("sqlite", u.String()); err != nil {
			return nil, fmt.Errorf("db: open database: %w", err)
		}
	} else {
		sqlDB = sql.OpenDB(wrappedConnector{dsn: u.String(), wrap: wrap})
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close() // the ping error is the one to report
		return nil, fmt.Errorf("db: connect to database: %w", err)
	}
	var foreignKeys int
	if err := sqlDB.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		_ = sqlDB.Close() // the foreign_keys error is the one to report
		if err != nil {
			return nil, fmt.Errorf("db: read SQLite foreign_keys: %w", err)
		}
		return nil, errors.New("db: SQLite foreign_keys is not enabled")
	}
	return newDB(sqlDB), nil
}

// wrappedConnector opens SQLite connections and passes each through wrap.
type wrappedConnector struct {
	dsn  string
	wrap func(driver.Conn) driver.Conn
}

func (c wrappedConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := (&sqlite.Driver{}).Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return c.wrap(conn), nil
}

func (c wrappedConnector) Driver() driver.Driver { return &sqlite.Driver{} }

// Open opens an existing database and checks that it is usable: the migration
// state is the pinned release and every column of every row struct exists
// (extra columns are accepted). Go cannot migrate, because upstream runs its
// migrations when it reconnects, so any other database is refused with
// ErrSchema instead of being run on a schema the code does not match. Open
// never creates, migrates or writes.
func Open(path string) (*DB, error) {
	d, err := Connect(path)
	if err != nil {
		return nil, err
	}
	if err := d.validate(context.Background()); err != nil {
		_ = d.Close() // the validation error is the one to report
		return nil, err
	}
	return d, nil
}

// Close closes the database.
func (d *DB) Close() error { return d.x.Close() }

func (d *DB) validate(ctx context.Context) error {
	var current, maxVersion string
	if err := d.Get(ctx, &current, "SELECT value FROM migrationsMeta WHERE key = 'version' LIMIT 1"); err != nil {
		return fmt.Errorf("%w: read migrationsMeta version: %v", ErrSchema, err)
	}
	if err := d.Get(ctx, &maxVersion, "SELECT value FROM migrationsMeta WHERE key = 'maxVersion' LIMIT 1"); err != nil {
		return fmt.Errorf("%w: read migrationsMeta maxVersion: %v", ErrSchema, err)
	}
	if current != version.Server || maxVersion != version.Server {
		return fmt.Errorf("%w: migration state is %q/%q; pinned baseline is %s", ErrSchema, current, maxVersion, version.Server)
	}
	for _, t := range tableRows {
		var have []string
		if err := d.Select(ctx, &have, "SELECT name FROM pragma_table_info(?)", t.table); err != nil {
			return fmt.Errorf("%w: inspect table %s: %v", ErrSchema, t.table, err)
		}
		present := make(map[string]bool, len(have))
		for _, name := range have {
			present[name] = true
		}
		for _, col := range d.columnsOf(reflect.TypeOf(t.row)) {
			if !present[col.name] {
				return fmt.Errorf("%w: table %s is missing column %s", ErrSchema, t.table, col.name)
			}
		}
	}
	return nil
}

// Create makes a new database file at path: the pinned schema, the migration
// rows of the pinned release and the given settings rows, in one transaction.
// The file is created exclusively, so a concurrent creator or an existing file
// yields an error that satisfies errors.Is(err, os.ErrExist) and the file is
// never touched. When anything else fails the half-made file is removed.
func Create(path string, defaults []Setting) (err error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("db: create database: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(path) // best effort; the creation error is the one to report
			_ = os.Remove(path + "-journal")
		}
	}()
	if err = file.Close(); err != nil {
		return fmt.Errorf("db: close new database file: %w", err)
	}
	d, err := Connect(path)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := d.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("db: close new database: %w", closeErr)
		}
	}()
	err = d.Tx(context.Background(), func(ctx context.Context) error {
		if _, err := d.Exec(ctx, Schema); err != nil {
			return fmt.Errorf("apply pinned schema: %w", err)
		}
		for _, key := range []string{"version", "maxVersion"} {
			if err := d.Insert(ctx, "migrationsMeta", MigrationsMeta{Key: key, Value: version.Server}); err != nil {
				return err
			}
		}
		now := Now()
		for _, setting := range defaults {
			setting.CreatedAt, setting.UpdatedAt = now, now
			if err := d.Insert(ctx, "settings", setting); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("db: initialise new database: %w", err)
	}
	return nil
}

// OpenOrCreate opens the database at path, creating it first with Create when
// the file does not exist. An existing file is validated and never modified.
// If another process is creating the file at the same moment, the validation
// is retried for a few seconds until that process has finished.
func OpenOrCreate(path string, defaults []Setting) (*DB, error) {
	err := Create(path, defaults)
	switch {
	case err == nil:
		slog.Info("database created with the pinned schema and default settings", "path", path)
	case !errors.Is(err, os.ErrExist):
		return nil, err
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		d, openErr := Open(path)
		if openErr == nil || err == nil || time.Now().After(deadline) {
			return d, openErr
		}
		if _, statErr := os.Stat(path); statErr != nil {
			return nil, openErr // the other creator gave up and removed the file
		}
		time.Sleep(10 * time.Millisecond)
	}
}
