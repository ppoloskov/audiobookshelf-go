// Package sqlupdate collects changed columns the way Sequelize's
// set()/changed() does and saves them with a fresh updatedAt.
package sqlupdate

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"

	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/store"
)

// Update is an ordered set of changed columns and their values.
type Update struct {
	Columns []string
	Values  []any
}

// Change records column when current and next differ and reports whether it
// did.
func (u *Update) Change(column string, current, next *string) bool {
	if samePointer(current, next) {
		return false
	}
	u.Columns = append(u.Columns, column)
	if next == nil {
		u.Values = append(u.Values, nil)
	} else {
		u.Values = append(u.Values, *next)
	}
	return true
}

// Force marks a column changed even when the value is the same
// (changed(column, true)): the update then still bumps updatedAt.
func (u *Update) Force(column string, next *string) {
	if i := slices.Index(u.Columns, column); i >= 0 {
		u.Values[i] = store.StringPtrValue(next)
		return
	}
	u.Columns = append(u.Columns, column)
	u.Values = append(u.Values, store.StringPtrValue(next))
}

// Save writes the changed columns plus updatedAt. table must be an internal
// constant; it is interpolated because SQL identifiers cannot be bound.
func (u *Update) Save(ctx context.Context, db *sql.DB, table, id string) error {
	if len(u.Columns) == 0 {
		return nil
	}
	sets := make([]string, 0, len(u.Columns)+1)
	for _, column := range u.Columns {
		sets = append(sets, column+"=?")
	}
	sets = append(sets, "updatedAt=?")
	args := append(append([]any{}, u.Values...), dbtime.Now(), id)
	query := fmt.Sprintf(`UPDATE %s SET %s WHERE id=?`, table, strings.Join(sets, ","))
	_, err := db.ExecContext(ctx, query, args...)
	return err
}

func samePointer(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
