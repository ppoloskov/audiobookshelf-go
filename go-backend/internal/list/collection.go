package list

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strings"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/item"
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

// open loads a collection: 404 when it is missing or its library is closed to
// the user, then 403 unless permitted.
func (h *Handler) open(ctx context.Context, user *auth.Principal, id string, permitted bool) (db.Collection, error) {
	var row db.Collection
	err := h.db.Get(ctx, &row, `SELECT * FROM collections WHERE id=?`, id)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !user.CanAccessLibrary(db.Val(row.LibraryID)) {
		return row, httpx.Error(http.StatusNotFound, "Collection not found")
	}
	if err == nil && !permitted {
		return row, httpx.Forbidden()
	}
	return row, err
}

// editable is open for a changing request with a body (update permission).
func (h *Handler) editable(w http.ResponseWriter, r *http.Request, user *auth.Principal) (db.Collection, collectionRequest, error) {
	row, err := h.open(r.Context(), user, r.PathValue("id"), user.CanUpdate())
	if err != nil {
		return row, collectionRequest{}, err
	}
	req, err := httpx.Decode[collectionRequest](w, r)
	return row, req, err
}

// viewOf builds the collection with its readable books in collection order.
func (h *Handler) viewOf(ctx context.Context, user *auth.Principal, row db.Collection) (collectionView, error) {
	var itemIDs []string
	err := h.db.Select(ctx, &itemIDs, `SELECT li.id FROM collectionBooks cb
JOIN libraryItems li ON li.mediaId = cb.bookId AND li.mediaType = 'book'
WHERE cb.collectionId = ? ORDER BY cb."order" ASC`, row.ID)
	if err != nil {
		return collectionView{}, err
	}
	loaded, err := item.Load(ctx, h.db, itemIDs, item.Options{})
	if err != nil {
		return collectionView{}, err
	}
	prefixes, books := h.settings.Server().SortingPrefixes, make([]item.Expanded, 0, len(loaded))
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

// finish answers with the collection as it is now and announces it as event
// (none when event is empty).
func (h *Handler) finish(w http.ResponseWriter, r *http.Request, user *auth.Principal, id, event string) error {
	var row db.Collection
	if err := h.db.Get(r.Context(), &row, `SELECT * FROM collections WHERE id=?`, id); err != nil {
		return err
	}
	view, err := h.viewOf(r.Context(), user, row)
	if err != nil {
		return err
	}
	if event != "" {
		h.emit.ToAll(event, view)
	}
	return httpx.JSON(w, r, view)
}

// collections lists the collections of libraryID ("" for every library) that
// the user can access.
func (h *Handler) collections(ctx context.Context, user *auth.Principal, libraryID string) ([]collectionView, error) {
	var rows []db.Collection
	if err := h.db.Select(ctx, &rows, `SELECT * FROM collections WHERE (?='' OR libraryId=?) ORDER BY createdAt ASC`, libraryID, libraryID); err != nil {
		return nil, err
	}
	views := make([]collectionView, 0, len(rows))
	for _, row := range rows {
		if user.CanAccessLibrary(db.Val(row.LibraryID)) {
			view, err := h.viewOf(ctx, user, row)
			if err != nil {
				return nil, err
			}
			views = append(views, view)
		}
	}
	return views, nil
}

func (h *Handler) listCollections(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	views, err := h.collections(r.Context(), user, "")
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
	views, err := h.collections(r.Context(), user, libraryID)
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
	row, err := h.open(r.Context(), user, r.PathValue("id"), true)
	if err != nil {
		return err
	}
	view, err := h.viewOf(r.Context(), user, row)
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

// insertBooks appends books to a collection that already has have of them.
func (h *Handler) insertBooks(ctx context.Context, collectionID string, have int, bookIDs []string) error {
	for i, bookID := range bookIDs {
		id, err := ids.New()
		if err != nil {
			return err
		}
		order := int64(have + i + 1)
		if err := h.db.Insert(ctx, "collectionBooks", &db.CollectionBook{ID: id, Order: &order, CreatedAt: db.Now(), BookID: &bookID, CollectionID: &collectionID}); err != nil {
			return err
		}
	}
	return nil
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
	name := cleanName(db.Val(req.Name))
	if name == "" || req.LibraryID == "" {
		return httpx.BadRequest("Invalid collection data")
	}
	if !user.CanAccessLibrary(req.LibraryID) {
		return httpx.Forbidden()
	}
	if len(req.Books) == 0 {
		return httpx.BadRequest("Invalid collection data. No books")
	}
	refs, err := h.itemRefs(r.Context(), req.Books...)
	if err != nil {
		return err
	}
	bookIDs := make([]string, len(req.Books))
	for i, id := range req.Books {
		ref, ok := refs[id]
		if !ok || ref.MediaType != "book" || ref.LibraryID != req.LibraryID {
			return httpx.BadRequest("Invalid collection data. Invalid books")
		}
		bookIDs[i] = ref.MediaID
	}
	id, now := "", db.Now()
	err = h.db.Tx(r.Context(), func(ctx context.Context) (err error) {
		if id, err = ids.New(); err != nil {
			return err
		}
		row := db.Collection{ID: id, Name: &name, Description: emptyAsNull(req.Description), LibraryID: &req.LibraryID, CreatedAt: now, UpdatedAt: now}
		if err = h.db.Insert(ctx, "collections", &row); err != nil {
			return err
		}
		return h.insertBooks(ctx, id, 0, bookIDs)
	})
	if err != nil {
		return err
	}
	return h.finish(w, r, user, id, "collection_added")
}

// updateCollection serves PATCH /api/collections/{id} (update permission):
// name, description and book order. An empty name is ignored.
func (h *Handler) updateCollection(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	row, req, err := h.editable(w, r, user)
	if err != nil {
		return err
	}
	refs, err := h.itemRefs(r.Context(), req.Books...)
	if err != nil {
		return err
	}
	changed := false
	err = h.db.Tx(r.Context(), func(ctx context.Context) error {
		if name := cleanName(db.Val(req.Name)); name != "" && name != db.Val(row.Name) {
			row.Name, changed = &name, true
		}
		if req.Description != nil && *req.Description != db.Val(row.Description) {
			row.Description, changed = req.Description, true
		}
		if len(req.Books) > 0 {
			wanted := make([]string, len(req.Books))
			for i, id := range req.Books {
				wanted[i] = refs[id].MediaID
			}
			moved, err := collectionLinks.reorder(ctx, h.db, row.ID, wanted, -1)
			if err != nil {
				return err
			}
			changed = changed || moved
		}
		if !changed {
			return nil
		}
		row.UpdatedAt = db.Now()
		return h.db.Update(ctx, "collections", &row, "name", "description", "updatedAt")
	})
	if err != nil {
		return err
	}
	return h.finish(w, r, user, row.ID, eventIf(changed, "collection_updated"))
}

// deleteCollection serves DELETE /api/collections/{id} (delete permission);
// its RSS feeds are closed with it.
func (h *Handler) deleteCollection(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	row, err := h.open(r.Context(), user, r.PathValue("id"), user.CanDelete())
	if err != nil {
		return err
	}
	view, err := h.viewOf(r.Context(), user, row)
	if err != nil {
		return err
	}
	var closed []db.Feed
	err = h.db.Tx(r.Context(), func(ctx context.Context) error {
		if err := h.db.Select(ctx, &closed, `SELECT * FROM feeds WHERE entityId=?`, row.ID); err != nil {
			return err
		}
		if _, err := h.db.Exec(ctx, `DELETE FROM feeds WHERE entityId=?`, row.ID); err != nil { // episodes cascade
			return err
		}
		_, err := h.db.Exec(ctx, `DELETE FROM collections WHERE id=?`, row.ID)
		return err
	})
	if err != nil {
		return err
	}
	for _, feed := range closed {
		c := closedFeed{ID: feed.ID, EntityType: feed.EntityType, EntityID: feed.EntityID, FeedURL: feed.FeedURL}
		c.Meta.Title, c.Meta.Description, c.Meta.PreventIndexing = feed.Title, feed.Description, bool(feed.PreventIndexing)
		c.Meta.OwnerName, c.Meta.OwnerEmail = feed.OwnerName, feed.OwnerEmail
		h.emit.ToAll("rss_feed_closed", c)
	}
	h.emit.ToAll("collection_removed", view)
	return httpx.Status(w, http.StatusOK)
}

// addCollectionBook serves POST /api/collections/{id}/book (update permission).
func (h *Handler) addCollectionBook(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	row, req, err := h.editable(w, r, user)
	if err != nil {
		return err
	}
	refs, err := h.itemRefs(r.Context(), req.ID)
	if err != nil {
		return err
	}
	ref, ok := refs[req.ID]
	switch {
	case !ok:
		return httpx.Error(http.StatusNotFound, "Book not found")
	case ref.LibraryID != db.Val(row.LibraryID):
		return httpx.BadRequest("Book in different library")
	case ref.MediaType != "book":
		return httpx.Error(http.StatusNotFound, "Book not found")
	}
	changed, err := h.changeBooks(r.Context(), row, []string{req.ID}, true)
	if err == nil && changed == 0 {
		err = httpx.BadRequest("Book already in collection")
	}
	return h.respondBooks(w, r, user, row.ID, changed, err)
}

// removeCollectionBook serves DELETE /api/collections/{id}/book/{itemID}.
func (h *Handler) removeCollectionBook(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	row, err := h.open(r.Context(), user, r.PathValue("id"), user.CanUpdate())
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
	changed, err := h.changeBooks(r.Context(), row, []string{itemID}, false)
	return h.respondBooks(w, r, user, row.ID, changed, err)
}

// batchCollectionBooks serves POST /api/collections/{id}/batch/add and /remove.
func (h *Handler) batchCollectionBooks(w http.ResponseWriter, r *http.Request, user *auth.Principal) error {
	action := r.PathValue("action")
	if action != "add" && action != "remove" {
		return httpx.NotFound()
	}
	row, req, err := h.editable(w, r, user)
	if err != nil {
		return err
	}
	if len(req.Books) == 0 {
		return httpx.BadRequest("Invalid request body")
	}
	changed, err := h.changeBooks(r.Context(), row, req.Books, action == "add")
	return h.respondBooks(w, r, user, row.ID, changed, err)
}

// changeBooks adds the books among the given items to the collection (those of
// its library, not yet in it) or removes them, and answers with the collection;
// a change that did nothing is not announced. Items that are not books are skipped.
func (h *Handler) changeBooks(ctx context.Context, row db.Collection, itemIDs []string, add bool) (changed int, err error) {
	refs, err := h.itemRefs(ctx, itemIDs...)
	if err != nil {
		return 0, err
	}
	var bookIDs []string // in request order
	for _, id := range itemIDs {
		if ref, ok := refs[id]; ok && ref.MediaType == "book" && (!add || ref.LibraryID == db.Val(row.LibraryID)) {
			bookIDs = append(bookIDs, ref.MediaID)
		}
	}
	if add && len(bookIDs) == 0 {
		return 0, httpx.BadRequest("Invalid request body. No valid books")
	}
	err = h.db.Tx(ctx, func(ctx context.Context) (err error) {
		if !add {
			changed, _, err = collectionLinks.remove(ctx, h.db, row.ID, bookIDs)
			return err
		}
		links, err := collectionLinks.list(ctx, h.db, row.ID)
		if err != nil {
			return err
		}
		var fresh []string
		for _, id := range bookIDs {
			if !slices.ContainsFunc(links, func(l link) bool { return l.MediaID == id }) && !slices.Contains(fresh, id) {
				fresh = append(fresh, id)
			}
		}
		changed = len(fresh)
		return h.insertBooks(ctx, row.ID, len(links), fresh)
	})
	return changed, err
}

// respondBooks answers a change of books: nothing changed is not announced.
func (h *Handler) respondBooks(w http.ResponseWriter, r *http.Request, user *auth.Principal, id string, changed int, err error) error {
	if err != nil {
		return err
	}
	return h.finish(w, r, user, id, eventIf(changed > 0, "collection_updated"))
}
