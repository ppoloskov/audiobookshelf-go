package admin

import (
	"context"
	"encoding/base64"
	"net/http"
	"slices"
	"strings"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
)

// A taxonomy ("tags" or "genres") is a list column that books and podcasts share.

func (h *Handler) listTaxonomy(w http.ResponseWriter, r *http.Request, kind string) error {
	values := []string{}
	for _, table := range []string{"books", "podcasts"} {
		var lists []db.JSON[[]string] // a malformed stored list contributes nothing
		if err := h.db.Select(r.Context(), &lists, "SELECT "+kind+" FROM "+table); err != nil {
			return httpx.Internal("database error", err)
		}
		for _, list := range lists {
			for _, value := range list.V {
				if !slices.Contains(values, value) {
					values = append(values, value)
				}
			}
		}
	}
	if kind == "tags" {
		slices.SortStableFunc(values, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	}
	return httpx.JSON(w, r, map[string][]string{kind: values}) // one key named by the route
}

// renameReply is the answer of a rename: the merged flag has the route's name.
type renameReply struct {
	TagMerged       *bool `json:"tagMerged,omitempty"`
	GenreMerged     *bool `json:"genreMerged,omitempty"`
	NumItemsUpdated int   `json:"numItemsUpdated"`
}

func (h *Handler) renameTaxonomy(w http.ResponseWriter, r *http.Request, kind string) error {
	body, err := httpx.Decode[map[string]string](w, r) // keys tag/newTag or genre/newGenre
	if err != nil {
		return err
	}
	oldKey, newKey := "tag", "newTag"
	if kind == "genres" {
		oldKey, newKey = "genre", "newGenre"
	}
	from, to := body[oldKey], body[newKey]
	if from == "" || to == "" {
		return httpx.BadRequest("")
	}
	merged := false
	changed, err := h.rewriteTaxonomy(r.Context(), kind, func(list []string) ([]string, bool) {
		merged = merged || slices.Contains(list, to)
		if !slices.Contains(list, from) {
			return nil, false
		}
		var out []string
		for _, value := range list {
			if value != from && !slices.Contains(out, value) {
				out = append(out, value)
			}
		}
		if !slices.Contains(out, to) {
			out = append(out, to)
		}
		return out, true
	})
	if err != nil {
		return httpx.Internal("failed to update item metadata", err)
	}
	reply := renameReply{NumItemsUpdated: changed}
	if kind == "tags" {
		reply.TagMerged = &merged
	} else {
		reply.GenreMerged = &merged
	}
	return httpx.JSON(w, r, reply)
}

func (h *Handler) deleteTaxonomy(w http.ResponseWriter, r *http.Request, kind string) error {
	// The client sends the name base64-encoded; PathValue has URL-unescaped it.
	segment := r.PathValue("name")
	raw, err := base64.StdEncoding.DecodeString(segment)
	if err != nil {
		if raw, err = base64.RawURLEncoding.DecodeString(segment); err != nil {
			return httpx.BadRequest("")
		}
	}
	target := string(raw)
	changed, err := h.rewriteTaxonomy(r.Context(), kind, func(list []string) ([]string, bool) {
		if !slices.Contains(list, target) {
			return nil, false
		}
		return slices.DeleteFunc(slices.Clone(list), func(v string) bool { return v == target }), true
	})
	if err != nil {
		return httpx.Internal("failed to update item metadata", err)
	}
	return httpx.JSON(w, r, struct {
		NumItemsUpdated int `json:"numItemsUpdated"`
	}{changed})
}

// rewriteTaxonomy saves the lists that edit changes (false leaves one alone),
// then has each changed item's metadata file and event written. It returns the
// number of items changed.
func (h *Handler) rewriteTaxonomy(ctx context.Context, kind string, edit func([]string) ([]string, bool)) (int, error) {
	defer h.deps.TaxonomyChanged()
	var changedItems []string
	err := h.db.Tx(ctx, func(ctx context.Context) error {
		for _, table := range []string{"books", "podcasts"} {
			var rows []struct {
				MediaID string            `db:"mediaId"`
				ItemID  string            `db:"itemId"`
				List    db.JSON[[]string] `db:"list"`
			}
			err := h.db.Select(ctx, &rows, "SELECT m.id AS mediaId, li.id AS itemId, m."+kind+" AS list FROM "+table+" m JOIN libraryItems li ON li.mediaId = m.id")
			if err != nil {
				return err
			}
			for _, row := range rows {
				edited, ok := edit(row.List.V) // a malformed or NULL list reads as empty and is left alone
				if !ok {
					continue
				}
				if _, err := h.db.Exec(ctx, "UPDATE "+table+" SET "+kind+" = ?, updatedAt = ? WHERE id = ?", db.JSON[[]string]{V: edited}, db.Now(), row.MediaID); err != nil {
					return err
				}
				changedItems = append(changedItems, row.ItemID)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, id := range changedItems {
		if err := h.deps.ItemChanged(ctx, id); err != nil {
			return len(changedItems), err
		}
	}
	return len(changedItems), nil
}
