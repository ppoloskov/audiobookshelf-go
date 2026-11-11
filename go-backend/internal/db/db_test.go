package db_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
)

func newAuthor(id, name string) db.Author {
	return db.Author{ID: id, Name: db.Ptr(name), CreatedAt: db.Now(), UpdatedAt: db.Now()}
}

func countAuthors(t *testing.T, ctx context.Context, d *db.DB) int {
	t.Helper()
	var n int
	if err := d.Get(ctx, &n, "SELECT COUNT(*) FROM authors"); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTx(t *testing.T) {
	errBoom := errors.New("boom")
	ctx := context.Background()

	t.Run("commit", func(t *testing.T) {
		d := dbtest.New(t)
		err := d.Tx(ctx, func(ctx context.Context) error {
			return d.Insert(ctx, "authors", newAuthor("a1", "A"))
		})
		if err != nil || countAuthors(t, ctx, d) != 1 {
			t.Fatalf("err = %v, authors = %d", err, countAuthors(t, ctx, d))
		}
	})

	t.Run("rollback on error", func(t *testing.T) {
		d := dbtest.New(t)
		err := d.Tx(ctx, func(ctx context.Context) error {
			if err := d.Insert(ctx, "authors", newAuthor("a1", "A")); err != nil {
				return err
			}
			// Inside the transaction the row is visible to every call that
			// carries the ctx.
			var n int
			if err := d.Get(ctx, &n, "SELECT COUNT(*) FROM authors"); err != nil || n != 1 {
				return fmt.Errorf("inside: count = %d, err = %v", n, err)
			}
			return errBoom
		})
		if !errors.Is(err, errBoom) {
			t.Fatalf("err = %v, want boom", err)
		}
		if n := countAuthors(t, ctx, d); n != 0 {
			t.Fatalf("authors after rollback = %d", n)
		}
	})

	t.Run("rollback on panic", func(t *testing.T) {
		d := dbtest.New(t)
		func() {
			defer func() {
				if recover() != "kaboom" {
					t.Error("the panic was swallowed")
				}
			}()
			_ = d.Tx(ctx, func(ctx context.Context) error {
				_ = d.Insert(ctx, "authors", newAuthor("a1", "A"))
				panic("kaboom")
			})
		}()
		if n := countAuthors(t, ctx, d); n != 0 {
			t.Fatalf("authors after panic = %d", n)
		}
	})

	t.Run("nested Tx joins the outer one", func(t *testing.T) {
		d := dbtest.New(t)
		err := d.Tx(ctx, func(ctx context.Context) error {
			if err := d.Tx(ctx, func(ctx context.Context) error {
				return d.Insert(ctx, "authors", newAuthor("a1", "A"))
			}); err != nil {
				return err
			}
			return errBoom // the inner Tx returned nil, the outer one still rolls back
		})
		if !errors.Is(err, errBoom) || countAuthors(t, ctx, d) != 0 {
			t.Fatalf("err = %v, authors = %d", err, countAuthors(t, ctx, d))
		}
	})

	t.Run("no deadlock on the single connection", func(t *testing.T) {
		d := dbtest.New(t)
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		err := d.Tx(ctx, func(ctx context.Context) error {
			if err := d.Insert(ctx, "authors", newAuthor("a1", "A")); err != nil {
				return err
			}
			var authors []db.Author
			if err := d.Select(ctx, &authors, "SELECT * FROM authors"); err != nil {
				return err
			}
			if len(authors) != 1 {
				return fmt.Errorf("authors = %d", len(authors))
			}
			_, err := d.Exec(ctx, "UPDATE authors SET name = ?", "B")
			return err
		})
		if err != nil {
			t.Fatalf("Tx: %v", err)
		}
	})

	t.Run("a transaction belongs to its DB", func(t *testing.T) {
		d1, d2 := dbtest.New(t), dbtest.New(t)
		err := d1.Tx(ctx, func(ctx context.Context) error {
			// d2 does not see d1's transaction, so this uses d2's pool.
			return d2.Insert(ctx, "authors", newAuthor("a1", "A"))
		})
		if err != nil || countAuthors(t, ctx, d2) != 1 {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestInExpansion(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		if err := d.Insert(ctx, "authors", newAuthor(id, "name "+id)); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name string
		ids  any
		want int
	}{
		{"two", []string{"a", "c"}, 2},
		{"one", []string{"b"}, 1},
		{"none match", []string{"zz"}, 0},
		{"empty", []string{}, 0},
		{"nil slice", []string(nil), 0},
		{"any slice", []any{"a", "b"}, 2},
	}
	for _, c := range cases {
		var authors []db.Author
		if err := d.Select(ctx, &authors, "SELECT * FROM authors WHERE id IN (?)", c.ids); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(authors) != c.want {
			t.Errorf("%s: got %d authors, want %d", c.name, len(authors), c.want)
		}
	}

	// A slice next to scalar arguments keeps the argument order.
	var n int
	if err := d.Get(ctx, &n, "SELECT COUNT(*) FROM authors WHERE name != ? AND id IN (?) AND id != ?", "name a", []string{"a", "b", "c"}, "c"); err != nil || n != 1 {
		t.Fatalf("mixed arguments: n = %d, err = %v", n, err)
	}
	// Exec expands too, and []byte is a scalar.
	res, err := d.Exec(ctx, "DELETE FROM authors WHERE id IN (?) AND asin IS NOT ?", []string{"a", "b"}, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if rows, _ := res.RowsAffected(); rows != 2 {
		t.Fatalf("deleted %d rows", rows)
	}
	if _, err := d.Exec(ctx, "DELETE FROM authors WHERE id IN (?)", []string{}); err != nil {
		t.Fatalf("empty list in Exec: %v", err)
	}
	if n := countAuthors(t, ctx, d); n != 1 {
		t.Fatalf("authors left = %d", n)
	}
}

func TestSelectFailsOnMissingStructField(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	if err := d.Insert(ctx, "authors", newAuthor("a1", "A")); err != nil {
		t.Fatal(err)
	}
	var narrow []struct {
		ID string `db:"id"`
	}
	err := d.Select(ctx, &narrow, "SELECT * FROM authors")
	if err == nil || !strings.Contains(err.Error(), "missing destination name") {
		t.Fatalf("scan of a struct that lacks columns: err = %v", err)
	}
}

func TestInsertAndUpdate(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	author := db.Author{ID: "a1", Name: db.Ptr("Name"), ASIN: db.Ptr("B0"), CreatedAt: db.Now(), UpdatedAt: db.Now()}
	if err := d.Insert(ctx, "authors", &author); err != nil {
		t.Fatal(err)
	}
	if err := d.Insert(ctx, "authors", author); err == nil {
		t.Fatal("inserting a duplicate id must fail")
	}
	if err := d.Insert(ctx, "authors", 42); err == nil {
		t.Fatal("inserting a non-struct must fail")
	}

	other := newAuthor("a2", "Other")
	if err := d.Insert(ctx, "authors", other); err != nil {
		t.Fatal(err)
	}

	// Named columns only: the other fields of the struct are not written.
	author.Name = db.Ptr("Changed")
	author.ASIN = db.Ptr("ignored")
	if err := d.Update(ctx, "authors", &author, "name"); err != nil {
		t.Fatal(err)
	}
	var got db.Author
	if err := d.Get(ctx, &got, "SELECT * FROM authors WHERE id = ?", "a1"); err != nil {
		t.Fatal(err)
	}
	if db.Val(got.Name) != "Changed" || db.Val(got.ASIN) != "B0" {
		t.Fatalf("after named update: name = %q, asin = %q", db.Val(got.Name), db.Val(got.ASIN))
	}
	if err := d.Get(ctx, &got, "SELECT * FROM authors WHERE id = ?", "a2"); err != nil || db.Val(got.Name) != "Other" {
		t.Fatalf("a2 changed: %v %+v", err, got)
	}

	// No columns named: every column except id.
	author.ASIN = db.Ptr("all")
	author.LastFirst = nil
	if err := d.Update(ctx, "authors", author); err != nil {
		t.Fatal(err)
	}
	if err := d.Get(ctx, &got, "SELECT * FROM authors WHERE id = ?", "a1"); err != nil || db.Val(got.ASIN) != "all" {
		t.Fatalf("after full update: %v %+v", err, got)
	}

	for name, call := range map[string]func() error{
		"unknown column": func() error { return d.Update(ctx, "authors", author, "nope") },
		"id column":      func() error { return d.Update(ctx, "authors", author, "id") },
		"no id struct":   func() error { return d.Update(ctx, "migrationsMeta", db.MigrationsMeta{Key: "k"}) },
		"not a struct":   func() error { return d.Update(ctx, "authors", "x") },
	} {
		if err := call(); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if err := d.Update(ctx, "authors", author, "nope"); err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Errorf("unknown column error = %v", err)
	}
}

// TestTriggerColumnsAreInsertOnly covers the dbwrite:"insert" columns of
// libraryItems, which triggers copy from books, podcasts and authors.
func TestTriggerColumnsAreInsertOnly(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	book := db.Book{ID: "b1", Title: db.Ptr("Book"), TitleIgnorePrefix: db.Ptr("Book"), CreatedAt: db.Now(), UpdatedAt: db.Now()}
	item := db.LibraryItem{ID: "li1", MediaID: db.Ptr("b1"), MediaType: db.Ptr("book"), CreatedAt: db.Now(), UpdatedAt: db.Now(),
		Title: db.Ptr("from Go"), TitleIgnorePrefix: db.Ptr("from Go"), AuthorNamesFirstLast: db.Ptr("from Go"), AuthorNamesLastFirst: db.Ptr("from Go")}
	if err := d.Insert(ctx, "books", book); err != nil {
		t.Fatal(err)
	}
	if err := d.Insert(ctx, "libraryItems", item); err != nil {
		t.Fatal(err)
	}
	var got db.LibraryItem
	if err := d.Get(ctx, &got, "SELECT * FROM libraryItems"); err != nil {
		t.Fatal(err)
	}
	if db.Val(got.Title) != "from Go" || db.Val(got.TitleIgnorePrefix) != "from Go" || db.Val(got.AuthorNamesFirstLast) != "from Go" || db.Val(got.AuthorNamesLastFirst) != "from Go" {
		t.Fatalf("Insert did not write the trigger columns: %+v", got)
	}

	// The trigger fills the columns when the book changes, and a full Update of
	// the item leaves them alone.
	book.Title = db.Ptr("Renamed")
	if err := d.Update(ctx, "books", book, "title"); err != nil {
		t.Fatal(err)
	}
	item.Title = db.Ptr("from Go again")
	if err := d.Update(ctx, "libraryItems", item); err != nil {
		t.Fatal(err)
	}
	if err := d.Get(ctx, &got, "SELECT * FROM libraryItems"); err != nil {
		t.Fatal(err)
	}
	if db.Val(got.Title) != "Renamed" {
		t.Fatalf("title = %q, want the trigger's copy", db.Val(got.Title))
	}
	if err := d.Update(ctx, "libraryItems", item, "title"); err == nil {
		t.Fatal("naming a trigger column must be an error")
	}
}
