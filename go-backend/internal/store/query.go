package store

import (
	"context"
	"database/sql"
	"slices"
)

const queryChunkSize = 500

// QueryIn runs prefix+"(?,?,...)"+suffix for ids in chunks of 500, calling fn
// for each row. The caller owns row decoding and must not issue another query
// on the same pool from fn.
func QueryIn(ctx context.Context, db *sql.DB, prefix, suffix string, ids []string, fn func(*sql.Rows) error) error {
	for chunk := range slices.Chunk(ids, queryChunkSize) {
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := db.QueryContext(ctx, prefix+"("+Placeholders(len(chunk))+")"+suffix, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			if err = fn(rows); err != nil {
				break
			}
		}
		if err == nil {
			err = rows.Err()
		}
		if cerr := rows.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}
