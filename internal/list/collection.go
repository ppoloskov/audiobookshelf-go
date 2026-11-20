package list

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strings"

	"monstrobook/internal/auth"
	"monstrobook/internal/db"
	"monstrobook/internal/httpx"
	"monstrobook/internal/item"
)

// collectionView is a collection with the books the requesting user may read.
type collectionView struct {
	ID          string          `json:"id"`
	LibraryID   string          `json:"libraryId"`
	Name        string          `json:"name"`
	Description *string         `json:"description"`
	Books       []item.Expanded `json:"books"`
	LastUpdate  db.Time         `json:"lastUpdate"`
	CreatedAt   db.Time         `json:"createdAt"`
	RSSFeed     *feedView       `json:"rssFeed,omitempty"`
}

// EventLibraryID tells the realtime hub which library the collection event
// belongs to, so only users who can access it receive it.
func (c collectionView) EventLibraryID() string { return c.LibraryID }

// feedView is the RSS feed a collection shows under "rssFeed" (Feed.toOldJSON;
// collections load their feed without episodes, so the list is empty).
type feedView struct {
	ID              string     `json:"id"`
	Slug            *string    `json:"slug"`
	UserID          *string    `json:"userId"`
	EntityType      *string    `json:"entityType"`
	EntityID        *string    `json:"entityId"`
	EntityUpdatedAt *int64     `json:"entityUpdatedAt"` // null when unset or 0
	CoverPath       *string    `json:"coverPath"`       // null when empty
	ServerAddress   *string    `json:"serverAddress"`
	FeedURL         *string    `json:"feedUrl"`
	Episodes        []struct{} `json:"episodes"`
	CreatedAt       db.Time    `json:"createdAt"`
	UpdatedAt       db.Time    `json:"updatedAt"`
	Meta            struct {
		Title           *string `json:"title"`
		Description     *string `json:"description"`
		Author          *string `json:"author"`
		ImageURL        *string `json:"imageUrl"`
		FeedURL         *string `json:"feedUrl"`
		Link            *string `json:"link"`
		Explicit        bool    `json:"explicit"`
		Type            *string `json:"type"`
		Language        *string `json:"language"`
		PreventIndexing bool    `json:"preventIndexing"`
		OwnerName       *string `json:"ownerName"`
		OwnerEmail      *string `json:"ownerEmail"`
	} `json:"meta"`
}

func newFeedView(f db.Feed) *feedView {
	v := &feedView{ID: f.ID, Slug: f.Slug, UserID: f.UserID, EntityType: f.EntityType, EntityID: f.EntityID, ServerAddress: f.ServerAddress,
		FeedURL: f.FeedURL, Episodes: []struct{}{}, CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt}
	if !f.EntityUpdatedAt.IsZero() && f.EntityUpdatedAt.Millis() != 0 {
		v.EntityUpdatedAt = db.Ptr(f.EntityUpdatedAt.Millis())
	}
	if db.Val(f.CoverPath) != "" {
		v.CoverPath = f.CoverPath
	}
	m := &v.Meta
	m.Title, m.Description, m.Author, m.ImageURL, m.FeedURL, m.Link = f.Title, f.Description, f.Author, f.ImageURL, f.FeedURL, f.SiteURL
	m.Explicit, m.Type, m.Language, m.PreventIndexing, m.OwnerName, m.OwnerEmail = bool(f.Explicit), f.PodcastType, f.Language, bool(f.PreventIndexing), f.OwnerName, f.OwnerEmail
	return v
}

// closedFeed is the minified feed of the rss_feed_closed event.
type closedFeed struct {
	ID         string  `json:"id"`
	EntityType *string `json:"entityType"`
	EntityID   *string `json:"entityId"`
	FeedURL    *string `json:"feedUrl"`
	Meta       struct {
		Title           *string `json:"title"`
		Description     *string `json:"description"`
		PreventIndexing bool    `json:"preventIndexing"`
		OwnerName       *string `json:"ownerName"`
		OwnerEmail      *string `json:"ownerEmail"`
	} `json:"meta"`
}

// collectionRequest is the body of create, update, add and the batch routes;
// Books are library item ids.
type collectionRequest struct {
	ID          string   `json:"id"` // the item to add
	Name        *string  `json:"name"`
	LibraryID   string   `json:"libraryId"`
	Description *string  `json:"description"`
	Books       []string `json:"books"`
}

func (h *Handler) collectionSpec() container[db.Collection, collectionView] {
	return container[db.Collection, collectionView]{
		h: h, kind: "collection", table: "collections", notFound: "Collection not found", links: collectionLinks, reorderUpdates: true,
		access: func(user *auth.Principal, row db.Collection, op listOp) error {
			if !user.CanAccessLibrary(db.Val(row.LibraryID)) {
				return httpx.Error(http.StatusNotFound, "Collection not found")
			}
			if op == opUpdate && !user.CanUpdate() || op == opDelete && !user.CanDelete() {
				return httpx.Forbidden()
			}
			return nil
		},
		libraryID: func(r db.Collection) string { return db.Val(r.LibraryID) },
		metaPtrs:  func(r *db.Collection) (**string, **string, *db.Time) { return &r.Name, &r.Description, &r.UpdatedAt },
		viewOf:    h.collectionViewOf,
		emit:      func(v collectionView, ev string) { h.emit.ToAll(ev, v) },
	}
}

// collectionViewOf builds the collection with its readable books in collection order.
func (h *Handler) collectionViewOf(ctx context.Context, user *auth.Principal, row db.Collection) (collectionView, error) {
	var itemIDs []string
	err := h.db.Select(ctx, &itemIDs, `SELECT li.id FROM collectionBooks cb
JOIN libraryItems li ON li.mediaId = cb.bookId AND li.mediaType = 'book'
WHERE cb.collectionId = ? ORDER BY cb."order" ASC`, row.ID)
	if err != nil {
		return collectionView{}, err
	}
	loaded, prefixes, err := h.loadItems(ctx, itemIDs, item.Options{})
	if err != nil {
		return collectionView{}, err
	}
	books := make([]item.Expanded, 0, len(loaded))
	for _, it := range loaded {
		if a := it.Access(); user.CanAccessItem(a.LibraryID, a.Explicit, a.Tags) {
			books = append(books, it.Expanded(prefixes))
		}
	}
	return collectionView{ID: row.ID, LibraryID: db.Val(row.LibraryID), Name: db.Val(row.Name), Description: row.Description,
		Books: books, LastUpdate: row.UpdatedAt, CreatedAt: row.CreatedAt}, nil
}

// feedOf is the RSS feed of the collection, nil without one.
func (h *Handler) feedOf(ctx context.Context, id string) (*feedView, error) {
	var f db.Feed
	err := h.db.Get(ctx, &f, `SELECT * FROM feeds WHERE entityType='collection' AND entityId=? LIMIT 1`, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = nil
		}
		return nil, err
	}
	return newFeedView(f), nil
}

func (h *Handler) listCollections(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	views, err := h.collectionSpec().list(r.Context(), user, `ORDER BY createdAt ASC`)
	if err != nil {
		return err
	}
	return httpx.JSON(w, r, struct {
		Collections []collectionView `json:"collections"`
	}{views})
}

// libraryCollections serves GET /api/libraries/{libraryID}/collections;
// include=rssfeed adds each collection's feed.
func (h *Handler) libraryCollections(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	libraryID, limit, page, err := h.libraryPage(r, user)
	if err != nil {
		return err
	}
	views, err := h.collectionSpec().list(r.Context(), user, `WHERE libraryId=? ORDER BY createdAt ASC`, libraryID)
	if err != nil {
		return err
	}
	query, include := r.URL.Query(), []string{}
	for _, v := range strings.Split(query.Get("include"), ",") {
		if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
			include = append(include, v)
		}
	}
	if slices.Contains(include, "rssfeed") {
		for i := range views {
			if views[i].RSSFeed, err = h.feedOf(r.Context(), views[i].ID); err != nil {
				return err
			}
		}
	}
	out := struct {
		Results  []collectionView `json:"results"`
		Total    int              `json:"total"`
		Limit    int              `json:"limit"`
		Page     int              `json:"page"`
		SortBy   *string          `json:"sortBy,omitempty"`
		SortDesc bool             `json:"sortDesc"`
		FilterBy *string          `json:"filterBy,omitempty"`
		Minified bool             `json:"minified"`
		Include  string           `json:"include"` // sort, filter, desc and minified are echoed, not applied
	}{Results: pageOf(views, limit, page), Total: len(views), Limit: limit, Page: page,
		SortDesc: query.Get("desc") == "1", Minified: query.Get("minified") == "1", Include: strings.Join(include, ",")}
	if v, ok := query["sort"]; ok {
		out.SortBy = &v[0]
	}
	if v, ok := query["filter"]; ok {
		out.FilterBy = &v[0]
	}
	return httpx.JSON(w, r, out)
}

// getCollection serves GET /api/collections/{id}; include=rssfeed adds the feed.
func (h *Handler) getCollection(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	row, err := h.collectionSpec().open(r.Context(), user, r.PathValue("id"), opRead)
	if err != nil {
		return err
	}
	view, err := h.collectionViewOf(r.Context(), user, row)
	if err != nil {
		return err
	}
	if slices.Contains(strings.Split(r.URL.Query().Get("include"), ","), "rssfeed") {
		if view.RSSFeed, err = h.feedOf(r.Context(), row.ID); err != nil {
			return err
		}
	}
	return httpx.JSON(w, r, view)
}

// createCollection serves POST /api/collections (update permission). Every
// book must be a book of the collection's library.
func (h *Handler) createCollection(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	if !user.CanUpdate() {
		return httpx.Forbidden()
	}
	req, err := httpx.Decode[collectionRequest](w, r)
	if err != nil {
		return err
	}
	name, err := validateCreate(user, req.Name, req.LibraryID, "Invalid collection data")
	if err != nil {
		return err
	}
	if len(req.Books) == 0 {
		return httpx.BadRequest("Invalid collection data. No books")
	}
	refs, err := h.itemRefs(r.Context(), req.Books...)
	if err != nil {
		return err
	}
	entries := make([]newEntry, len(req.Books))
	for i, id := range req.Books {
		ref, ok := refs[id]
		if !ok || ref.MediaType != "book" || ref.LibraryID != req.LibraryID {
			return httpx.BadRequest("Invalid collection data. Invalid books")
		}
		entries[i] = newEntry{ref.MediaID, "book"}
	}
	return h.collectionSpec().create(w, r, user, func(id string, now db.Time) db.Collection {
		return db.Collection{ID: id, Name: &name, Description: emptyAsNull(req.Description), LibraryID: &req.LibraryID, CreatedAt: now, UpdatedAt: now}
	}, entries)
}

// updateCollection serves PATCH /api/collections/{id} (update permission):
// name, description and book order. An empty name is ignored.
func (h *Handler) updateCollection(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	c := h.collectionSpec()
	row, req, err := openBody[collectionRequest](c, w, r, user, opUpdate)
	if err != nil {
		return err
	}
	refs, err := h.itemRefs(r.Context(), req.Books...)
	if err != nil {
		return err
	}
	wanted := make([]string, len(req.Books))
	for i, id := range req.Books {
		wanted[i] = refs[id].MediaID
	}
	return c.update(w, r, user, row, row.ID, req.Name, req.Description, wanted, -1, nil)
}

// deleteCollection serves DELETE /api/collections/{id} (delete permission);
// its RSS feeds are closed with it.
func (h *Handler) deleteCollection(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	var closed []db.Feed
	return h.collectionSpec().remove(w, r, user, func(ctx context.Context, row db.Collection) error {
		if err := h.db.Select(ctx, &closed, `SELECT * FROM feeds WHERE entityId=?`, row.ID); err != nil {
			return err
		}
		_, err := h.db.Exec(ctx, `DELETE FROM feeds WHERE entityId=?`, row.ID) // episodes cascade
		return err
	}, func() {
		for _, feed := range closed {
			c := closedFeed{ID: feed.ID, EntityType: feed.EntityType, EntityID: feed.EntityID, FeedURL: feed.FeedURL}
			c.Meta.Title, c.Meta.Description, c.Meta.PreventIndexing = feed.Title, feed.Description, bool(feed.PreventIndexing)
			c.Meta.OwnerName, c.Meta.OwnerEmail = feed.OwnerName, feed.OwnerEmail
			h.emit.ToAll("rss_feed_closed", c)
		}
	})
}

// addCollectionBook serves POST /api/collections/{id}/book (update permission).
func (h *Handler) addCollectionBook(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	c := h.collectionSpec()
	row, req, err := openBody[collectionRequest](c, w, r, user, opUpdate)
	if err != nil {
		return err
	}
	refs, err := h.itemRefs(r.Context(), req.ID)
	if err != nil {
		return err
	}
	ref, ok := refs[req.ID]
	switch {
	case !ok || ref.MediaType != "book":
		return httpx.Error(http.StatusNotFound, "Book not found")
	case ref.LibraryID != db.Val(row.LibraryID):
		return httpx.BadRequest("Book in different library")
	}
	return c.addEntries(w, r, user, row.ID, []newEntry{{ref.MediaID, "book"}}, "Book already in collection", nil)
}

// removeCollectionBook serves DELETE /api/collections/{id}/book/{itemID}.
func (h *Handler) removeCollectionBook(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	c := h.collectionSpec()
	row, err := c.open(r.Context(), user, r.PathValue("id"), opUpdate)
	if err != nil {
		return err
	}
	itemID := r.PathValue("itemID")
	refs, err := h.itemRefs(r.Context(), itemID)
	if err != nil {
		return err
	}
	if _, ok := refs[itemID]; !ok {
		return httpx.NotFound()
	}
	return h.removeCollectionBooks(w, r, user, row.ID, refs, []string{itemID})
}

// batchCollectionBooks serves POST /api/collections/{id}/batch/add and /remove.
func (h *Handler) batchCollectionBooks(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	action := r.PathValue("action")
	if action != "add" && action != "remove" {
		return httpx.NotFound()
	}
	c := h.collectionSpec()
	row, req, err := openBody[collectionRequest](c, w, r, user, opUpdate)
	if err != nil {
		return err
	}
	if len(req.Books) == 0 {
		return httpx.BadRequest("Invalid request body")
	}
	refs, err := h.itemRefs(r.Context(), req.Books...)
	if err != nil {
		return err
	}
	if action == "remove" {
		return h.removeCollectionBooks(w, r, user, row.ID, refs, req.Books)
	}
	_, entries := filterBookRefs(refs, req.Books, db.Val(row.LibraryID))
	if len(entries) == 0 {
		return httpx.BadRequest("Invalid request body. No valid books")
	}
	return c.addEntries(w, r, user, row.ID, entries, "", nil)
}

func filterBookRefs(refs map[string]itemRef, itemIDs []string, libraryID string) (bookIDs []string, entries []newEntry) {
	for _, id := range itemIDs {
		if ref, ok := refs[id]; ok && ref.MediaType == "book" && (libraryID == "" || ref.LibraryID == libraryID) {
			bookIDs = append(bookIDs, ref.MediaID)
			entries = append(entries, newEntry{ref.MediaID, "book"})
		}
	}
	return bookIDs, entries
}

// removeCollectionBooks deletes the books among itemIDs (non-books are skipped)
// and answers with the collection, emitting collection_updated only on change.
func (h *Handler) removeCollectionBooks(w http.ResponseWriter, r *http.Request, user *auth.Principal, id string, refs map[string]itemRef, itemIDs []string) error {
	bookIDs, _ := filterBookRefs(refs, itemIDs, "")
	removed := 0
	err := h.db.Tx(r.Context(), func(ctx context.Context) (err error) {
		removed, _, err = collectionLinks.remove(ctx, h.db, id, bookIDs, true)
		return err
	})
	if err != nil {
		return err
	}
	c := h.collectionSpec()
	return c.finish(w, r, user, id, eventIf(removed > 0, "collection_updated"), nil)
}
