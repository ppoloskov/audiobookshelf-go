package lists

import (
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strings"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/logging"
	"audiobookshelf-compat/internal/store"
)

func (h *Handler) createCollection(w http.ResponseWriter, r *http.Request) {
	user := requestUser(r)
	if !boolValue(store.DecodeJSONObject(user.Permissions)["update"]) {
		httputil.SendStatus(w, 403)
		return
	}
	body, err := httputil.DecodeObjectRequest(r)
	if err != nil {
		httputil.SendText(w, 400, "Invalid collection data")
		return
	}
	name, _ := body["name"].(string)
	name = cleanName(name)
	libraryID, _ := body["libraryId"].(string)
	description, descOK := body["description"].(string)
	if name == "" || libraryID == "" {
		httputil.SendText(w, 400, "Invalid collection data")
		return
	}
	if jsonvalue.Truthy(body["description"]) && !descOK {
		http.Error(w, "Invalid collection description", 400)
		return
	}
	// Collection.create persists `description || null` upstream; an empty
	// string therefore round-trips as JSON null, not as an empty string
	// (store.TextOrNull writes NULL for it).
	if !h.canAccessLibrary(user, libraryID) {
		httputil.SendStatus(w, 403)
		return
	}
	bookIDs := jsonvalue.Strings(body["books"])
	if len(bookIDs) == 0 {
		http.Error(w, "Invalid collection data. No books", 400)
		return
	}
	mediaIDs := make([]string, len(bookIDs))
	for i, itemID := range bookIDs {
		var mediaID, mediaType, lib string
		err := h.db.QueryRowContext(r.Context(), `SELECT mediaId,mediaType,libraryId FROM libraryItems WHERE id=?`, itemID).Scan(&mediaID, &mediaType, &lib)
		if err != nil || mediaType != "book" || lib != libraryID {
			http.Error(w, "Invalid collection data. Invalid books", 400)
			return
		}
		mediaIDs[i] = mediaID
	}
	id, err := ids.New()
	if err != nil {
		writeInternalError(w, r, "Failed to create collection", err)
		return
	}
	now := dbtime.Now()
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeInternalError(w, r, "Failed to create collection", err)
		return
	}
	defer tx.Rollback()
	q := sqlc.New(tx)
	if err = q.InsertCollection(r.Context(), sqlc.InsertCollectionParams{ID: id, Name: name, Description: store.TextOrNull(description), LibraryID: libraryID, Createdat: now, Updatedat: now}); err != nil {
		writeInternalError(w, r, "Failed to create collection", err)
		return
	}
	for i, bookID := range mediaIDs {
		entryID, genErr := ids.New()
		if genErr != nil {
			writeInternalError(w, r, "Failed to create collection", genErr)
			return
		}
		if err = q.InsertCollectionBook(r.Context(), sqlc.InsertCollectionBookParams{ID: entryID, CollectionID: id, BookID: bookID, Position: int64(i + 1), Createdat: now}); err != nil {
			writeInternalError(w, r, "Failed to create collection", err)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		writeInternalError(w, r, "Failed to create collection", err)
		return
	}
	dto, _, err := h.collection(r.Context(), id, user)
	if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	if h.emit != nil {
		h.emit("*", "collection_added", dto)
	}
	httputil.WriteJSON(w, 200, dto)
}

// collectionLibraryID is the library of a collection; sql.ErrNoRows when the
// collection does not exist.
func (h *Handler) updateCollection(w http.ResponseWriter, r *http.Request, user store.User, id string) {
	c, err := h.queries().GetCollection(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		httputil.SendStatus(w, 404)
		return
	} else if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	libraryID, name, desc := c.Libraryid.String, c.Name.String, c.Description
	if !h.canAccessLibrary(user, libraryID) {
		httputil.SendStatus(w, 404)
		return
	}
	if !boolValue(store.DecodeJSONObject(user.Permissions)["update"]) {
		httputil.SendStatus(w, 403)
		return
	}
	oldName, oldDesc := name, desc
	body, err := httputil.DecodeObjectRequest(r)
	if err != nil {
		http.Error(w, "Invalid request", 400)
		return
	}
	if val, ok := body["description"]; ok && val != nil {
		if s, ok := val.(string); ok {
			desc = sql.NullString{String: s, Valid: true}
		} else {
			logging.RequestDebug(r, "responding 500: description is not a string")
			http.Error(w, "database write failed", 500)
			return
		}
	} else if _, ok := body["description"]; ok {
		desc = sql.NullString{}
	}
	if val, ok := body["name"].(string); ok {
		name = cleanName(val)
	}
	changed := name != oldName || desc != oldDesc
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeInternalError(w, r, "database write failed", err)
		return
	}
	defer tx.Rollback()
	books := jsonvalue.Strings(body["books"])
	if len(books) > 0 {
		linkRows, err := sqlc.New(tx).ListCollectionBookLinks(r.Context(), id)
		if err != nil {
			writeInternalError(w, r, "database write failed", err)
			return
		}
		type collectionLink struct {
			linkID, itemID string
			order          int
		}
		links := make([]collectionLink, 0, len(linkRows))
		for _, row := range linkRows {
			links = append(links, collectionLink{linkID: row.LinkID, itemID: row.ItemID, order: int(row.Position.Int64)})
		}
		indexOf := func(item string) int {
			for i, wanted := range books {
				if wanted == item {
					return i
				}
			}
			return -1
		}
		sort.SliceStable(links, func(i, j int) bool { return indexOf(links[i].itemID) < indexOf(links[j].itemID) })
		for i, link := range links {
			if link.order != i+1 {
				if err := sqlc.New(tx).UpdateCollectionBookOrder(r.Context(), sqlc.UpdateCollectionBookOrderParams{Position: int64(i + 1), ID: link.linkID}); err != nil {
					writeInternalError(w, r, "database write failed", err)
					return
				}
				changed = true
			}
		}
	}
	if changed {
		if err := sqlc.New(tx).UpdateCollection(r.Context(), sqlc.UpdateCollectionParams{Name: name, Description: desc, Updatedat: dbtime.Now(), ID: id}); err != nil {
			writeInternalError(w, r, "database write failed", err)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		writeInternalError(w, r, "database write failed", err)
		return
	}
	dto, _, err := h.collection(r.Context(), id, user)
	if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	if changed && h.emit != nil {
		h.emit("*", "collection_updated", dto)
	}
	httputil.WriteJSON(w, 200, dto)
	return
}

// batchCollectionBooks mirrors the legacy batch endpoints. Invalid identifiers
// are filtered before the query; adding a batch with no valid books is an
// error, while removing unknown books is an idempotent no-op.
func (h *Handler) batchCollectionBooks(w http.ResponseWriter, r *http.Request) {
	user, id, action := requestUser(r), r.PathValue("id"), r.PathValue("action")
	if action != "add" && action != "remove" {
		notImplemented(w)
		return
	}
	add := action == "add"
	libraryID, err := h.collectionLibraryID(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		sendEntityNotFound(w, "Collection not found")
		return
	} else if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	if !h.canAccessLibrary(user, libraryID) {
		sendEntityNotFound(w, "Collection not found")
		return
	}
	if !boolValue(store.DecodeJSONObject(user.Permissions)["update"]) {
		httputil.SendStatus(w, 403)
		return
	}
	body, err := httputil.DecodeObjectRequest(r)
	if err != nil {
		if add {
			httputil.SendText(w, 400, "Invalid request body")
		} else {
			logging.RequestDebug(r, "responding 500: the request body has no valid books array")
			http.Error(w, "Invalid request body", 500)
		}
		return
	}
	itemIDs := validCollectionBookIDs(body["books"])
	if len(itemIDs) == 0 {
		if add {
			httputil.SendText(w, 400, "Invalid request body")
		} else {
			logging.RequestDebug(r, "responding 500: the request body has no valid books array")
			http.Error(w, "Invalid request body", 500)
		}
		return
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(itemIDs)), ",")
	args := make([]any, 0, len(itemIDs)+2)
	for _, itemID := range itemIDs {
		args = append(args, itemID)
	}
	query := `SELECT id,mediaId FROM libraryItems WHERE id IN (` + placeholders + `) AND mediaType='book'`
	if add {
		query += ` AND libraryId=?`
		args = append(args, libraryID)
	}
	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	mediaByItem := make(map[string]string)
	mediaSet := make(map[string]bool)
	for rows.Next() {
		var itemID, mediaID string
		if err := rows.Scan(&itemID, &mediaID); err != nil {
			rows.Close()
			writeInternalError(w, r, "database read failed", err)
			return
		}
		mediaByItem[itemID] = mediaID
		mediaSet[mediaID] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeInternalError(w, r, "database read failed", err)
		return
	}
	rows.Close()
	if add && len(mediaByItem) == 0 {
		httputil.SendText(w, 400, "Invalid request body. No valid books")
		return
	}
	if add {
		count, err := h.queries().CountCollectionBooks(r.Context(), id)
		if err != nil {
			writeInternalError(w, r, "database read failed", err)
			return
		}
		order := int(count)
		members, err := h.queries().ListCollectionBookMediaIDs(r.Context(), id)
		if err != nil {
			writeInternalError(w, r, "database read failed", err)
			return
		}
		existing := map[string]bool{}
		for _, m := range members {
			existing[m.String] = true
		}
		tx, err := h.db.BeginTx(r.Context(), nil)
		if err != nil {
			writeInternalError(w, r, "database write failed", err)
			return
		}
		defer tx.Rollback()
		changed := false
		for _, itemID := range itemIDs {
			mediaID := mediaByItem[itemID]
			if mediaID == "" || existing[mediaID] {
				continue
			}
			order++
			changed = true
			existing[mediaID] = true
			entryID, err := ids.New()
			if err != nil {
				writeInternalError(w, r, "database write failed", err)
				return
			}
			if err := sqlc.New(tx).InsertCollectionBook(r.Context(), sqlc.InsertCollectionBookParams{ID: entryID, CollectionID: id, BookID: mediaID, Position: int64(order), Createdat: dbtime.Now()}); err != nil {
				writeInternalError(w, r, "database write failed", err)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			writeInternalError(w, r, "database write failed", err)
			return
		}
		h.writeCollectionAfterBatch(w, r, user, id, changed)
		return
	}

	entries, err := h.queries().ListCollectionBooks(r.Context(), id)
	if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	type link struct {
		id, mediaID string
		order       int
	}
	links := make([]link, 0, len(entries))
	for _, e := range entries {
		links = append(links, link{id: e.ID, mediaID: e.Bookid.String, order: int(e.Order.Int64)})
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeInternalError(w, r, "database write failed", err)
		return
	}
	defer tx.Rollback()
	changed, order := false, 1
	for _, entry := range links {
		if mediaSet[entry.mediaID] {
			if err := sqlc.New(tx).DeleteCollectionBook(r.Context(), entry.id); err != nil {
				writeInternalError(w, r, "database write failed", err)
				return
			}
			changed = true
			continue
		}
		if entry.order != order {
			if err := sqlc.New(tx).UpdateCollectionBookOrder(r.Context(), sqlc.UpdateCollectionBookOrderParams{Position: int64(order), ID: entry.id}); err != nil {
				writeInternalError(w, r, "database write failed", err)
				return
			}
			changed = true
		}
		order++
	}
	if err := tx.Commit(); err != nil {
		writeInternalError(w, r, "database write failed", err)
		return
	}
	h.writeCollectionAfterBatch(w, r, user, id, changed)
}

// validCollectionBookIDs matches CollectionController.addBatch: accept only
// non-empty string IDs, ignoring malformed entries in the submitted array.
func validCollectionBookIDs(raw any) []string {
	values, _ := raw.([]any)
	ids := make([]string, 0, len(values))
	for _, value := range values {
		if id, ok := value.(string); ok && id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func (h *Handler) writeCollectionAfterBatch(w http.ResponseWriter, r *http.Request, user store.User, id string, changed bool) {
	dto, _, err := h.collection(r.Context(), id, user)
	if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	if changed && h.emit != nil {
		h.emit("*", "collection_updated", dto)
	}
	httputil.WriteJSON(w, 200, dto)
	return
}

func (h *Handler) addCollectionBook(w http.ResponseWriter, r *http.Request) {
	user, id := requestUser(r), r.PathValue("id")
	libraryID, err := h.collectionLibraryID(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		sendEntityNotFound(w, "Collection not found")
		return
	}
	if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	if !h.canAccessLibrary(user, libraryID) {
		sendEntityNotFound(w, "Collection not found")
		return
	}
	if !boolValue(store.DecodeJSONObject(user.Permissions)["update"]) {
		httputil.SendStatus(w, 403)
		return
	}
	body, err := httputil.DecodeObjectRequest(r)
	if err != nil {
		httputil.SendStatus(w, 400)
		return
	}
	itemID, _ := body["id"].(string)
	var mediaID, mediaType, lib string
	err = h.db.QueryRowContext(r.Context(), `SELECT mediaId,mediaType,libraryId FROM libraryItems WHERE id=?`, itemID).Scan(&mediaID, &mediaType, &lib)
	if err != nil {
		http.Error(w, "Book not found", 404)
		return
	}
	if lib != libraryID {
		http.Error(w, "Book in different library", 400)
		return
	}
	if mediaType != "book" {
		http.Error(w, "Book not found", 404)
		return
	}
	count, _ := h.queries().CountCollectionBooks(r.Context(), id)
	exists, _ := h.queries().CountCollectionBook(r.Context(), sqlc.CountCollectionBookParams{CollectionID: id, BookID: mediaID})
	if exists > 0 {
		http.Error(w, "Book already in collection", 400)
		return
	}
	now := dbtime.Now()
	entryID, err := ids.New()
	if err != nil {
		writeInternalError(w, r, "database write failed", err)
		return
	}
	if err = h.queries().InsertCollectionBook(r.Context(), sqlc.InsertCollectionBookParams{ID: entryID, CollectionID: id, BookID: mediaID, Position: count + 1, Createdat: now}); err != nil {
		writeInternalError(w, r, "database write failed", err)
		return
	}
	dto, _, err := h.collection(r.Context(), id, user)
	if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	if h.emit != nil {
		h.emit("*", "collection_updated", dto)
	}
	httputil.WriteJSON(w, 200, dto)
	return
}

func (h *Handler) removeCollectionBook(w http.ResponseWriter, r *http.Request) {
	user, id, itemID := requestUser(r), r.PathValue("id"), r.PathValue("itemID")
	libraryID, err := h.collectionLibraryID(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		sendEntityNotFound(w, "Collection not found")
		return
	}
	if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	if !h.canAccessLibrary(user, libraryID) {
		sendEntityNotFound(w, "Collection not found")
		return
	}
	if !boolValue(store.DecodeJSONObject(user.Permissions)["update"]) {
		httputil.SendStatus(w, 403)
		return
	}
	var mediaID string
	err = h.db.QueryRowContext(r.Context(), `SELECT mediaId FROM libraryItems WHERE id=?`, itemID).Scan(&mediaID)
	if errors.Is(err, sql.ErrNoRows) {
		httputil.SendStatus(w, 404)
		return
	}
	if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeInternalError(w, r, "database write failed", err)
		return
	}
	defer tx.Rollback()
	q := sqlc.New(tx)
	res, err := q.DeleteCollectionBookByMediaID(r.Context(), sqlc.DeleteCollectionBookByMediaIDParams{CollectionID: id, BookID: mediaID})
	if err != nil {
		writeInternalError(w, r, "database write failed", err)
		return
	}
	removed, _ := res.RowsAffected()
	entries, err := q.ListCollectionBooks(r.Context(), id)
	if err != nil {
		writeInternalError(w, r, "database write failed", err)
		return
	}
	for seq, e := range entries {
		_ = q.UpdateCollectionBookOrder(r.Context(), sqlc.UpdateCollectionBookOrderParams{Position: int64(seq + 1), ID: e.ID})
	}
	if err = tx.Commit(); err != nil {
		writeInternalError(w, r, "database write failed", err)
		return
	}
	dto, _, err := h.collection(r.Context(), id, user)
	if err != nil {
		writeInternalError(w, r, "database read failed", err)
		return
	}
	if removed > 0 && h.emit != nil {
		h.emit("*", "collection_updated", dto)
	}
	httputil.WriteJSON(w, 200, dto)
	return
}

func (h *Handler) collectionRSSFeed(r *http.Request, collectionID string) (map[string]any, error) {
	feed, err := h.queries().GetFeedByEntity(r.Context(), sqlc.GetFeedByEntityParams{EntityType: "collection", EntityID: collectionID})
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	eps, err := h.queries().ListFeedEpisodesNewestFirst(r.Context(), feed.ID)
	if err != nil {
		return nil, err
	}
	episodes := make([]any, 0, len(eps))
	for _, e := range eps {
		episodes = append(episodes, map[string]any{"id": e.ID, "title": store.NullString(e.Title), "description": store.NullString(e.Description), "enclosure": map[string]any{"url": store.NullString(e.Enclosureurl), "size": store.NullInt64(e.Enclosuresize), "type": store.NullString(e.Enclosuretype)}, "pubDate": store.NullString(e.Pubdate), "link": store.NullString(e.Siteurl), "author": store.NullString(e.Author), "explicit": e.Explicit.Int64 != 0, "duration": nullFloat(e.Duration), "season": store.NullString(e.Season), "episode": store.NullString(e.Episode), "episodeType": store.NullString(e.Episodetype), "fullPath": store.NullString(e.Filepath)})
	}
	return map[string]any{"id": feed.ID, "slug": feed.Slug.String, "userId": feed.Userid.String, "entityType": feed.Entitytype.String, "entityId": feed.Entityid.String, "entityUpdatedAt": dbtime.Millis(feed.Entityupdatedat), "coverPath": store.NullString(feed.Coverpath), "meta": map[string]any{"title": feed.Title.String, "description": feed.Description.String, "author": feed.Author.String, "imageUrl": store.NullString(feed.Imageurl), "feedUrl": store.NullString(feed.Feedurl), "link": store.NullString(feed.Siteurl), "explicit": feed.Explicit.Int64 != 0, "type": feed.Podcasttype.String, "language": feed.Language.String, "preventIndexing": feed.Preventindexing.Int64 != 0, "ownerName": feed.Ownername.String, "ownerEmail": feed.Owneremail.String}, "serverAddress": feed.Serveraddress.String, "feedUrl": feed.Feedurl.String, "episodes": episodes, "createdAt": dbtime.Millis(feed.Createdat), "updatedAt": dbtime.Millis(feed.Updatedat)}, nil
}

// nullFloat is a nullable FLOAT column as a JSON value: NULL becomes nil.
func nullFloat(v sql.NullFloat64) any {
	if !v.Valid {
		return nil
	}
	return v.Float64
}
