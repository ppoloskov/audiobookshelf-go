package catalog

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/itemwrite"
	"audiobookshelf-compat/internal/textclean"
)

// env is what the follow-up of an author or series edit touches.
func (h *Handler) env() itemwrite.Env {
	return itemwrite.Env{DB: h.db, Filters: h.filters, Emit: events.Emitter(h.emit)}
}

// saveMetadataFiles is libraryItem.saveMetadataFile for each item.
func (h *Handler) saveMetadataFiles(ctx context.Context, itemIDs []string) error {
	if h.itemChanged == nil {
		return nil
	}
	for _, id := range itemIDs {
		if err := h.itemChanged(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// ItemChanged is the end of an item mutation: the metadata file is rewritten
// and item_updated carries the expanded item. The admin tag and genre routes
// use it too.
func (h *Handler) ItemChanged(ctx context.Context, itemID string) error {
	if err := h.saveMetadataFiles(ctx, []string{itemID}); err != nil {
		return err
	}
	if h.emit != nil {
		if expanded, err := h.ItemJSON(ctx, itemID, true); err == nil {
			h.emit("*", "item_updated", expanded)
		}
	}
	return nil
}

// storedRow is one row of a table as read from the database.
type storedRow struct {
	names  []string
	values []any
}

// get is the stored value of a column (nil for NULL or an unknown column).
func (r storedRow) get(column string) any {
	for i, name := range r.names {
		if name == column {
			return r.values[i]
		}
	}
	return nil
}

// rowColumns reads one row of table.
func rowColumns(ctx context.Context, tx *sql.Tx, table, id string) (storedRow, error) {
	rows, err := tx.QueryContext(ctx, `SELECT * FROM `+table+` WHERE id=?`, id)
	if err != nil {
		return storedRow{}, err
	}
	defer rows.Close()
	names, err := rows.Columns()
	if err != nil {
		return storedRow{}, err
	}
	if !rows.Next() {
		return storedRow{}, sql.ErrNoRows
	}
	values := make([]any, len(names))
	targets := make([]any, len(names))
	for i := range values {
		targets[i] = &values[i]
	}
	if err := rows.Scan(targets...); err != nil {
		return storedRow{}, err
	}
	return storedRow{names: names, values: values}, rows.Err()
}

// columnSet collects the columns of a media row that an update changes against
// the stored row; PATCH media, batch update and quick match all build one.
type columnSet struct {
	current storedRow
	set     map[string]any
}

func newColumnSet(current storedRow) *columnSet {
	return &columnSet{current: current, set: map[string]any{}}
}

func (c *columnSet) names() []string {
	names := make([]string, 0, len(c.set))
	for name := range c.set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// put records value when it differs from the stored one: JSON columns compare
// by content, booleans by value, the rest as stored text.
func (c *columnSet) put(name string, value any) {
	stored := c.current.get(name)
	switch v := value.(type) {
	case []string:
		if storedJSONText(stored) != jsonValue(v) {
			c.set[name] = jsonValue(v)
		}
	case bool:
		if storedBool(stored) != v {
			c.set[name] = v
		}
	default:
		if !reflect.DeepEqual(storedString(stored), v) {
			c.set[name] = v
		}
	}
}

// text is a string column of Book.updateFromRequest (Book.js:382): the raw
// request value is compared with the stored one before sanitizing, and ""
// stores NULL.
func (c *columnSet) text(column string, requested any, ok bool) {
	if !ok || storedString(c.current.get(column)) == requested {
		return
	}
	if column == "description" && requested != nil {
		requested = textclean.Sanitize(fmt.Sprint(requested))
	}
	if requested == "" {
		requested = nil
	}
	c.set[column] = requested
}

// bookFields are the columns Book.updateFromRequest (Book.js:370-431) changes.
func (c *columnSet) bookFields(metadata, payload map[string]json.RawMessage) {
	for _, key := range []string{"title", "subtitle", "publishedYear", "publishedDate", "publisher", "description", "isbn", "asin", "language"} {
		value, ok := requestString(metadata, key)
		c.text(key, value, ok)
	}
	c.requestFields(metadata, payload, "explicit", "abridged")
	if value, ok := requestStringArray(metadata, "narrators"); ok {
		c.put("narrators", value)
	}
}

// podcastFields are the columns Podcast.updateFromRequest (Podcast.js:210-292) changes.
func (c *columnSet) podcastFields(metadata, payload map[string]json.RawMessage) {
	for key, column := range map[string]string{"title": "title", "author": "author", "releaseDate": "releaseDate", "feedUrl": "feedURL", "imageUrl": "imageURL", "description": "description", "itunesPageUrl": "itunesPageURL", "itunesId": "itunesId", "itunesArtistId": "itunesArtistId", "language": "language", "type": "podcastType"} {
		value, ok := requestString(metadata, key)
		c.text(column, value, ok)
	}
	c.requestFields(metadata, payload, "explicit")
	if value, ok := requestBool(payload, "autoDownloadEpisodes"); ok {
		c.put("autoDownloadEpisodes", value)
	}
	if raw, ok := payload["autoDownloadSchedule"]; ok {
		var value string
		if json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value) != "" {
			c.text("autoDownloadSchedule", value, true)
		}
	}
	for _, key := range []string{"maxEpisodesToKeep", "maxNewEpisodesToDownload"} {
		var v json.Number
		if raw, ok := payload[key]; ok && json.Unmarshal(raw, &v) == nil {
			if n, e := strconv.ParseInt(v.String(), 10, 64); e == nil {
				if stored, isInt := c.current.get(key).(int64); !isInt || stored != n {
					c.set[key] = n
				}
			}
		}
	}
}

// requestFields are the flags, genres and tags both media types share.
func (c *columnSet) requestFields(metadata, payload map[string]json.RawMessage, flags ...string) {
	for _, key := range flags {
		if value, ok := requestBool(metadata, key); ok {
			c.put(key, value)
		}
	}
	if value, ok := requestStringArray(metadata, "genres"); ok {
		c.put("genres", value)
	}
	if value, ok := requestStringArray(payload, "tags"); ok {
		c.put("tags", value)
	}
}

// update writes the collected columns and the update stamp.
func (c *columnSet) update(ctx context.Context, tx *sql.Tx, table, id string) error {
	sets := make([]string, 0, len(c.set)+1)
	args := make([]any, 0, len(c.set)+2)
	for _, name := range c.names() {
		sets = append(sets, `"`+name+`"=?`)
		args = append(args, c.set[name])
	}
	sets = append(sets, `"updatedAt"=?`)
	args = append(args, dbNow(), id)
	_, err := tx.ExecContext(ctx, `UPDATE `+table+` SET `+strings.Join(sets, ",")+` WHERE id=?`, args...)
	return err
}

// storedString is a TEXT column value as read into an interface.
func storedString(v any) any {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}

// storedBool is a BOOLEAN column value.
func storedBool(v any) bool {
	switch x := v.(type) {
	case int64:
		return x != 0
	case bool:
		return x
	case float64:
		return x != 0
	}
	return false
}

// storedJSONText is JSON.stringify of a JSON column's parsed value.
func storedJSONText(v any) string {
	text, ok := storedString(v).(string)
	if !ok {
		return "null"
	}
	var compact bytes.Buffer
	if json.Compact(&compact, []byte(text)) != nil {
		return text
	}
	return compact.String()
}
