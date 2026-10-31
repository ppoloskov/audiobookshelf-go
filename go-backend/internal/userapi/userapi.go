// Package userapi contains a narrow SQLite-backed implementation of the
// user-facing API routes that can be reproduced without the Node managers.
package userapi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/items"
	"audiobookshelf-compat/internal/progressdto"
	"audiobookshelf-compat/internal/store"
	"golang.org/x/crypto/bcrypt"
)

type EmitFunc func(userID, event string, payload any)

type Handler struct {
	db                 *sql.DB
	cfg                config.Config
	emit               EmitFunc
	items              items.Reader
	invalidateSessions func(context.Context, string, string) (string, string, error)
	changePassword     func(context.Context, string, string, string) error
	setRefreshCookie   func(http.ResponseWriter, *http.Request, string)
	usersOnline        func() []map[string]any
	openSessions       func() []map[string]any
}

func New(db *sql.DB, cfg config.Config, emit func(userID, event string, payload any), itemReader items.Reader) (*Handler, error) {
	if db == nil {
		return nil, errors.New("userapi: database is nil")
	}
	if itemReader == nil {
		return nil, errors.New("userapi: item reader is nil")
	}
	return &Handler{db: db, cfg: cfg, emit: EmitFunc(emit), items: itemReader}, nil
}

func (h *Handler) SetCredentialHooks(invalidate func(context.Context, string, string) (string, string, error), changePassword func(context.Context, string, string, string) error, setCookie func(http.ResponseWriter, *http.Request, string)) {
	h.invalidateSessions, h.changePassword, h.setRefreshCookie = invalidate, changePassword, setCookie
}

func (h *Handler) SetOnlineData(users func() []map[string]any, sessions func() []map[string]any) {
	h.usersOnline, h.openSessions = users, sessions
}

// Handle handles implemented paths relative to /api and reports whether the
// path belongs to this package. False leaves the route to the root 501 handler.
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request, user store.User) bool {
	path := strings.TrimSuffix(r.URL.Path, "/")
	path = strings.TrimPrefix(path, "/api")
	if path == "" {
		path = "/"
	}
	switch {
	case path == "/me" && r.Method == http.MethodGet:
		h.getMe(w, r, user)
	case path == "/me/sessions" && r.Method == http.MethodGet:
		h.getSessions(w, r, user)
	case path == "/me/password" && r.Method == http.MethodPatch:
		h.updatePassword(w, r, user)
	case strings.HasPrefix(path, "/me/sessions/") && r.Method == http.MethodDelete:
		h.deleteSession(w, r, user, strings.TrimPrefix(path, "/me/sessions/"))
	case path == "/me/listening-sessions" && r.Method == http.MethodGet:
		h.writeListeningSessions(w, r, user.ID, "", false)
	case strings.HasPrefix(path, "/me/item/listening-sessions/") && r.Method == http.MethodGet:
		h.getItemListeningSessions(w, r, user, strings.TrimPrefix(path, "/me/item/listening-sessions/"))
	case path == "/me/listening-stats" && r.Method == http.MethodGet:
		h.writeListeningStats(w, r, user.ID)
	case path == "/me/items-in-progress" && r.Method == http.MethodGet:
		h.getItemsInProgress(w, r, user)
	case strings.HasPrefix(path, "/me/series/") && r.Method == http.MethodGet:
		h.updateSeriesContinueListening(w, r, user, strings.TrimPrefix(path, "/me/series/"))
	case strings.HasPrefix(path, "/me/stats/year/") && r.Method == http.MethodGet:
		h.getYearStats(w, r, user, strings.TrimPrefix(path, "/me/stats/year/"))
	case path == "/me/ereader-devices" && r.Method == http.MethodPost:
		h.updateEReaderDevices(w, r, user)
	case path == "/me/progress" && r.Method == http.MethodGet:
		h.getAllProgress(w, r, user)
	case strings.HasPrefix(path, "/me/progress/"):
		h.handleProgress(w, r, user, strings.TrimPrefix(path, "/me/progress/"))
	case path == "/me/bookmarks" && r.Method == http.MethodGet:
		h.getBookmarks(w, r, user, "")
	case strings.HasPrefix(path, "/me/bookmarks/") && r.Method == http.MethodGet:
		h.getBookmarks(w, r, user, strings.TrimPrefix(path, "/me/bookmarks/"))
	case strings.HasPrefix(path, "/me/item/") && strings.Contains(path, "/bookmark"):
		h.handleBookmark(w, r, user, strings.TrimPrefix(path, "/me/item/"))
	case path == "/api-keys" || strings.HasPrefix(path, "/api-keys/"):
		h.handleAPIKeys(w, r, user, strings.TrimPrefix(path, "/api-keys"))
	case path == "/users" && r.Method == http.MethodGet:
		h.listUsers(w, r, user)
	case path == "/users" && r.Method == http.MethodPost:
		h.createUser(w, r, user)
	case path == "/users/online" && r.Method == http.MethodGet:
		h.getOnlineUsers(w, user)
	case strings.HasPrefix(path, "/users/"):
		tail := strings.TrimPrefix(path, "/users/")
		h.handleUser(w, r, user, tail)
	case strings.HasPrefix(path, "/collections") || strings.HasPrefix(path, "/playlists"):
		return h.collectionPlaylist(w, r, user, path)
	default:
		return false
	}
	return true
}

func (h *Handler) getMe(w http.ResponseWriter, r *http.Request, user store.User) {
	progress, err := h.progressRows(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	serialized, err := h.browserUserDB(r.Context(), user, progress, jsonArray(user.Bookmarks))
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	writeJSON(w, 200, serialized)
}

func (h *Handler) getOnlineUsers(w http.ResponseWriter, user store.User) {
	if !isAdmin(user) {
		sendStatus(w, 403)
		return
	}
	users, sessions := []map[string]any{}, []map[string]any{}
	if h.usersOnline != nil {
		users = h.usersOnline()
	}
	if h.openSessions != nil {
		sessions = h.openSessions()
	}
	writeJSON(w, 200, map[string]any{"usersOnline": users, "openSessions": sessions})
}

func (h *Handler) updatePassword(w http.ResponseWriter, r *http.Request, user store.User) {
	if user.Type == "guest" {
		sendStatus(w, 403)
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		http.Error(w, "Missing or invalid password or new password", 400)
		return
	}
	oldRaw, hasOld := body["password"]
	newRaw, hasNew := body["newPassword"]
	if !hasOld || !hasNew {
		http.Error(w, "Missing or invalid password or new password", 400)
		return
	}
	oldPassword, oldOK := oldRaw.(string)
	newPassword, newOK := newRaw.(string)
	if oldRaw == nil {
		oldOK = true
	}
	if newRaw == nil {
		newOK = true
	}
	if !oldOK || !newOK {
		http.Error(w, "Missing or invalid password or new password", 400)
		return
	}
	if h.changePassword == nil || h.invalidateSessions == nil {
		writeNotImplemented(w)
		return
	}
	if err := h.changePassword(r.Context(), user.ID, oldPassword, newPassword); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	accessToken, refreshToken, err := h.invalidateSessions(r.Context(), user.ID, refreshTokenFromRequest(r))
	if err != nil {
		http.Error(w, "database write failed", 500)
		return
	}
	if refreshToken != "" && r.Header.Get("x-refresh-token") != "" {
		writeJSON(w, 200, map[string]any{"success": true, "user": map[string]any{"accessToken": accessToken, "refreshToken": refreshToken}})
		return
	}
	if refreshToken != "" && h.setRefreshCookie != nil {
		h.setRefreshCookie(w, r, refreshToken)
	}
	sendStatus(w, 200)
}

func (h *Handler) getAllProgress(w http.ResponseWriter, r *http.Request, user store.User) {
	progress, err := h.progressRows(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	writeJSON(w, 200, map[string]any{"mediaProgress": progress})
}

func (h *Handler) getBookmarks(w http.ResponseWriter, r *http.Request, user store.User, libraryItemID string) {
	bookmarks := jsonArray(user.Bookmarks)
	if libraryItemID == "" {
		writeJSON(w, 200, map[string]any{"bookmarks": bookmarks})
		return
	}
	access, err := h.canAccessItem(r.Context(), user, libraryItemID)
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	if !access.exists {
		sendStatus(w, 404)
		return
	}
	if !access.allowed {
		sendStatus(w, 403)
		return
	}
	filtered := make([]any, 0)
	for _, entry := range bookmarks {
		if bm, ok := entry.(map[string]any); ok && bm["libraryItemId"] == libraryItemID {
			filtered = append(filtered, bm)
		}
	}
	writeJSON(w, 200, map[string]any{"bookmarks": filtered})
}

func (h *Handler) handleProgress(w http.ResponseWriter, r *http.Request, user store.User, tail string) {
	parts := strings.Split(tail, "/")
	if len(parts) == 2 && parts[1] == "remove-from-continue-listening" && r.Method == http.MethodGet {
		res, err := h.db.ExecContext(r.Context(), `UPDATE mediaProgresses SET hideFromContinueListening = 1, updatedAt = ? WHERE id = ? AND userId = ?`, dbtime.Now(), parts[0], user.ID)
		if err != nil {
			http.Error(w, "database write failed", 500)
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			sendStatus(w, 404)
			return
		}
		h.emitUserUpdate(r.Context(), user)
		payload, err := h.userPayload(r.Context(), user)
		if err != nil {
			http.Error(w, "database read failed", 500)
			return
		}
		writeJSON(w, 200, payload)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if len(parts) < 1 || len(parts) > 2 {
			sendStatus(w, 404)
			return
		}
		var episode any
		if len(parts) == 2 {
			episode = parts[1]
		}
		query := `SELECT id,userId,mediaItemId,mediaItemType,duration,currentTime,isFinished,hideFromContinueListening,ebookLocation,ebookProgress,finishedAt,extraData,createdAt,updatedAt FROM mediaProgresses WHERE userId = ? AND json_extract(extraData,'$.libraryItemId') = ?`
		args := []any{user.ID, parts[0]}
		if episode != nil {
			query += ` AND mediaItemId = ?`
			args = append(args, episode)
		}
		query += ` ORDER BY createdAt ASC LIMIT 1`
		p, err := scanProgress(h.db.QueryRowContext(r.Context(), query, args...))
		if errors.Is(err, sql.ErrNoRows) {
			sendStatus(w, 404)
			return
		}
		if err != nil {
			http.Error(w, "database read failed", 500)
			return
		}
		writeJSON(w, 200, p)
	case http.MethodDelete:
		if len(parts) != 1 {
			sendStatus(w, 404)
			return
		}
		res, err := h.db.ExecContext(r.Context(), `DELETE FROM mediaProgresses WHERE id = ? AND userId = ?`, parts[0], user.ID)
		if err != nil {
			http.Error(w, "database write failed", 500)
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			sendStatus(w, 404)
			return
		}
		sendStatus(w, 200)
	case http.MethodPatch:
		if len(parts) == 2 && parts[0] == "batch" && parts[1] == "update" {
			var batch []map[string]any
			defer r.Body.Close()
			dec := json.NewDecoder(r.Body)
			dec.UseNumber()
			if err := dec.Decode(&batch); err != nil || len(batch) == 0 {
				http.Error(w, "Missing request payload", 400)
				return
			}
			updated := false
			for _, payload := range batch {
				if h.updateProgress(r.Context(), user, payload) == nil {
					updated = true
				}
			}
			if updated {
				h.emitUserUpdate(r.Context(), user)
			}
			sendStatus(w, 200)
			return
		}
		if len(parts) < 1 || len(parts) > 2 {
			sendStatus(w, 404)
			return
		}
		payload, err := decodeBody(r)
		if err != nil {
			http.Error(w, "Invalid request body", 400)
			return
		}
		payload["libraryItemId"] = parts[0]
		if len(parts) == 2 {
			payload["episodeId"] = parts[1]
		}
		err = h.updateProgress(r.Context(), user, payload)
		if err != nil {
			writeError(w, err)
			return
		}
		h.emitUserUpdate(r.Context(), user)
		sendStatus(w, 200)
	default:
		sendStatus(w, 404)
	}
}

func (h *Handler) updateProgress(ctx context.Context, user store.User, p map[string]any) error {
	itemID, _ := p["libraryItemId"].(string)
	episodeID, _ := p["episodeId"].(string)
	if itemID == "" {
		return apiError{400, "Invalid library item id"}
	}
	var mediaItemID, mediaType, libraryID string
	var podcastID any
	if episodeID != "" {
		err := h.db.QueryRowContext(ctx, `SELECT pe.id, pe.podcastId, li.id, li.libraryId FROM podcastEpisodes pe JOIN podcasts p ON p.id = pe.podcastId JOIN libraryItems li ON li.mediaId = p.id AND li.mediaType = 'podcast' WHERE pe.id = ?`, episodeID).Scan(&mediaItemID, &podcastID, &itemID, &libraryID)
		if errors.Is(err, sql.ErrNoRows) {
			return apiError{404, "Episode not found"}
		}
		if err != nil {
			return err
		}
		mediaType = "podcastEpisode"
	} else {
		err := h.db.QueryRowContext(ctx, `SELECT mediaId, mediaType, libraryId FROM libraryItems WHERE id = ?`, itemID).Scan(&mediaItemID, &mediaType, &libraryID)
		if errors.Is(err, sql.ErrNoRows) {
			return apiError{404, "Library item not found"}
		}
		if err != nil {
			return err
		}
		if mediaType != "book" {
			return apiError{400, "Library item is not a book"}
		}
		mediaType = "book"
	}
	permission := permissions(user)
	if !canAccessLibrary(permission, libraryID) {
		return apiError{403, "Forbidden"}
	}
	now := dbtime.Now()
	var id string
	var oldDuration, oldCurrent, oldEbook sql.NullFloat64
	var oldEbookLocation sql.NullString
	var oldFinished bool
	var oldHide bool
	var oldFinishedAt any
	var oldExtra []byte
	err := h.db.QueryRowContext(ctx, `SELECT id,duration,currentTime,isFinished,hideFromContinueListening,ebookLocation,ebookProgress,finishedAt,extraData FROM mediaProgresses WHERE userId = ? AND mediaItemId = ? AND mediaItemType = ? LIMIT 1`, user.ID, mediaItemID, mediaType).Scan(&id, &oldDuration, &oldCurrent, &oldFinished, &oldHide, &oldEbookLocation, &oldEbook, &oldFinishedAt, &oldExtra)
	if errors.Is(err, sql.ErrNoRows) {
		duration := number(p["duration"], 0)
		current := number(p["currentTime"], 0)
		ebook := number(p["ebookProgress"], 0)
		finished, _ := p["isFinished"].(bool)
		hide, _ := p["hideFromContinueListening"].(bool)
		progress := number(p["progress"], 0)
		if finished {
			progress = 1
		}
		extra, _ := json.Marshal(map[string]any{"libraryItemId": itemID, "progress": progress})
		finishedAt := any(nil)
		if finished {
			finishedAt = now
		}
		_, err = h.db.ExecContext(ctx, `INSERT INTO mediaProgresses (id,userId,mediaItemId,mediaItemType,duration,currentTime,isFinished,hideFromContinueListening,ebookLocation,ebookProgress,finishedAt,extraData,podcastId,createdAt,updatedAt) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, newUUID(), user.ID, mediaItemID, mediaType, duration, current, finished, hide, p["ebookLocation"], ebook, finishedAt, extra, podcastID, timestampString(p["createdAt"], now), now)
		if err != nil {
			return err
		}
		return nil
	}
	if err != nil {
		return err
	}
	current := oldCurrent.Float64
	if v, ok := p["currentTime"]; ok {
		current = number(v, oldCurrent.Float64)
	}
	duration := oldDuration.Float64
	if v, ok := p["duration"]; ok {
		duration = number(v, oldDuration.Float64)
	}
	finished := oldFinished
	if v, ok := p["isFinished"].(bool); ok {
		finished = v
	}
	if explicitFinished, ok := p["isFinished"].(bool); ok && !explicitFinished && oldFinished {
		current = 0
	}
	hide := oldHide
	if v, ok := p["hideFromContinueListening"].(bool); ok {
		hide = v
	}
	if _, changed := p["currentTime"]; changed && !hide {
		hide = false
	}
	var extra map[string]any
	_ = json.Unmarshal(oldExtra, &extra)
	if extra == nil {
		extra = map[string]any{"libraryItemId": itemID}
	}
	if v, ok := p["progress"]; ok {
		extra["progress"] = number(v, 0)
	}
	if explicitFinished, ok := p["isFinished"].(bool); ok && !explicitFinished && oldFinished {
		extra["progress"] = 0
	}
	if finished {
		extra["progress"] = 1
	}
	shouldFinish := false
	if duration > 0 {
		thresholdPercent := number(p["markAsFinishedPercentComplete"], 0)
		if thresholdPercent > 0 {
			shouldFinish = thresholdPercent/100 < current/duration
		} else {
			shouldFinish = duration-current < number(p["markAsFinishedTimeRemaining"], 10)
		}
	}
	if !finished && shouldFinish {
		finished = true
		extra["progress"] = 1
	} else if finished && p["currentTime"] != nil && !shouldFinish {
		finished = false
	}
	finishedAt := oldFinishedAt
	if finished && !oldFinished {
		finishedAt = timestampString(p["finishedAt"], now)
	} else if !finished && oldFinished {
		finishedAt = nil
	} else if p["finishedAt"] != nil {
		finishedAt = timestampString(p["finishedAt"], now)
	}
	extraJSON, _ := json.Marshal(extra)
	ebookLocation := any(nil)
	if oldEbookLocation.Valid {
		ebookLocation = oldEbookLocation.String
	}
	if v, ok := p["ebookLocation"]; ok {
		ebookLocation = v
	}
	ebookProgress := oldEbook.Float64
	if v, ok := p["ebookProgress"]; ok {
		ebookProgress = number(v, oldEbook.Float64)
	}
	_, err = h.db.ExecContext(ctx, `UPDATE mediaProgresses SET duration=?,currentTime=?,isFinished=?,hideFromContinueListening=?,ebookLocation=?,ebookProgress=?,finishedAt=?,extraData=?,updatedAt=? WHERE id=? AND userId=?`, duration, current, finished, hide, ebookLocation, ebookProgress, finishedAt, extraJSON, timestampString(p["lastUpdate"], now), id, user.ID)
	return err
}

func (h *Handler) handleBookmark(w http.ResponseWriter, r *http.Request, user store.User, tail string) {
	parts := strings.Split(tail, "/")
	if len(parts) < 2 || parts[1] != "bookmark" {
		sendStatus(w, 404)
		return
	}
	itemID := parts[0]
	access, err := h.canAccessItem(r.Context(), user, itemID)
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	if !access.exists {
		sendStatus(w, 404)
		return
	}
	if !access.allowed {
		sendStatus(w, 403)
		return
	}
	bookmarks := jsonArray(user.Bookmarks)
	switch r.Method {
	case http.MethodPost, http.MethodPatch:
		if len(parts) != 2 {
			sendStatus(w, 404)
			return
		}
		body, err := decodeBody(r)
		if err != nil {
			http.Error(w, "Invalid request body", 400)
			return
		}
		timeValue, ok := finiteNumber(body["time"])
		if !ok {
			http.Error(w, "Invalid time", 400)
			return
		}
		title, ok := body["title"].(string)
		if !ok || title == "" {
			http.Error(w, "Invalid title", 400)
			return
		}
		index := bookmarkIndex(bookmarks, itemID, timeValue)
		if r.Method == http.MethodPatch && index < 0 {
			sendStatus(w, 404)
			return
		}
		var entry map[string]any
		if index < 0 {
			entry = map[string]any{"libraryItemId": itemID, "time": timeValue, "title": title, "createdAt": time.Now().UnixMilli()}
			bookmarks = append(bookmarks, entry)
		} else {
			entry, _ = bookmarks[index].(map[string]any)
			entry["title"] = title
		}
		if err := h.saveBookmarks(r.Context(), user.ID, bookmarks); err != nil {
			http.Error(w, "database write failed", 500)
			return
		}
		user.Bookmarks, _ = json.Marshal(bookmarks)
		h.emitUserUpdate(r.Context(), user)
		writeJSON(w, 200, entry)
	case http.MethodDelete:
		if len(parts) != 3 {
			sendStatus(w, 404)
			return
		}
		timeValue, err := strconv.ParseFloat(parts[2], 64)
		if err != nil || math.IsNaN(timeValue) || math.IsInf(timeValue, 0) {
			http.Error(w, "Invalid time", 400)
			return
		}
		index := bookmarkIndex(bookmarks, itemID, timeValue)
		if index < 0 {
			sendStatus(w, 404)
			return
		}
		bookmarks = append(bookmarks[:index], bookmarks[index+1:]...)
		if err := h.saveBookmarks(r.Context(), user.ID, bookmarks); err != nil {
			http.Error(w, "database write failed", 500)
			return
		}
		user.Bookmarks, _ = json.Marshal(bookmarks)
		h.emitUserUpdate(r.Context(), user)
		sendStatus(w, 200)
	default:
		sendStatus(w, 404)
	}
}

func (h *Handler) saveBookmarks(ctx context.Context, userID string, bookmarks []any) error {
	encoded, err := json.Marshal(bookmarks)
	if err != nil {
		return err
	}
	_, err = h.db.ExecContext(ctx, `UPDATE users SET bookmarks = ?, updatedAt = ? WHERE id = ?`, encoded, dbtime.Now(), userID)
	return err
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request, current store.User) {
	if !isAdmin(current) {
		sendStatus(w, 403)
		return
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT id,username,email,pash,type,token,isActive,isLocked,lastSeen,permissions,bookmarks,extraData,createdAt,updatedAt FROM users`)
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	defer rows.Close()
	users := make([]any, 0)
	hideRootToken := current.Type != "root"
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			http.Error(w, "database read failed", 500)
			return
		}
		users = append(users, minimalBrowserUser(u, hideRootToken))
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	writeJSON(w, 200, map[string]any{"users": users})
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request, current store.User) {
	if !isAdmin(current) {
		sendStatus(w, 403)
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		sendText(w, 400, "Username and password are required")
		return
	}
	username, _ := body["username"].(string)
	password, _ := body["password"].(string)
	if username == "" || password == "" {
		sendText(w, 400, "Username and password are required")
		return
	}
	typ, _ := body["type"].(string)
	if typ == "" {
		typ = "user"
	}
	if typ != "user" && typ != "admin" && typ != "guest" {
		sendText(w, 400, "Invalid account type")
		return
	}
	var exists int
	err = h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM users WHERE username = ?`, username).Scan(&exists)
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	if exists > 0 {
		sendText(w, 400, "Username already taken")
		return
	}
	permissions := defaultPermissions(typ)
	if reqPerms, ok := body["permissions"].(map[string]any); ok {
		for k, v := range reqPerms {
			if _, exists := permissions[k]; exists {
				if b, ok := v.(bool); ok {
					permissions[k] = b
				}
			}
		}
	}
	for _, key := range []string{"librariesAccessible", "itemTagsSelected"} {
		if value, ok := body[key]; ok {
			if validStringArray(value) {
				permissions[key] = value
			}
		} else if reqPerms, ok := body["permissions"].(map[string]any); ok {
			if value, ok := reqPerms[key]; ok && validStringArray(value) {
				permissions[key] = value
			}
		}
	}
	encodedPerms, _ := json.Marshal(permissions)
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 8)
	if err != nil {
		http.Error(w, "Failed to save new user", 500)
		return
	}
	userID := newUUID()
	email, _ := body["email"].(string)
	// Upstream user create via Sequelize omits undefined fields; an absent
	// email is stored as SQL NULL (models/User.js), and the browser DTO then
	// serializes it as null.
	var emailValue any = nil
	if email != "" {
		emailValue = email
	}
	active, _ := body["isActive"].(bool)
	now := dbtime.Now()
	token, err := h.signToken(map[string]any{"userId": userID, "username": username})
	if err != nil {
		http.Error(w, "Failed to save new user", 500)
		return
	}
	if _, err = h.db.ExecContext(r.Context(), `INSERT INTO users (id,username,email,pash,type,token,isActive,isLocked,lastSeen,permissions,bookmarks,extraData,createdAt,updatedAt) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, userID, username, emailValue, string(hash), typ, token, active, false, nil, encodedPerms, `[]`, `{"seriesHideFromContinueListening":[]}`, now, now); err != nil {
		http.Error(w, "Failed to save new user", 500)
		return
	}
	newUser, err := h.userByID(r.Context(), userID)
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	serialized, err := h.browserUserDB(r.Context(), newUser, []any{}, []any{})
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	if h.emit != nil {
		h.emit("admin", "user_added", serialized)
	}
	writeJSON(w, 200, map[string]any{"user": serialized})
}

func (h *Handler) handleUser(w http.ResponseWriter, r *http.Request, current store.User, tail string) {
	parts := strings.Split(tail, "/")
	id := parts[0]
	if id == "" {
		return
	}
	if len(parts) == 2 && (parts[1] == "listening-sessions" || parts[1] == "listening-stats") && r.Method == http.MethodGet {
		if !isAdmin(current) && current.ID != id {
			sendStatus(w, 403)
			return
		}
		if _, err := h.userByID(r.Context(), id); errors.Is(err, sql.ErrNoRows) {
			sendStatus(w, 404)
			return
		} else if err != nil {
			http.Error(w, "database read failed", 500)
			return
		}
		if parts[1] == "listening-stats" {
			h.writeListeningStats(w, r, id)
		} else {
			h.writeListeningSessions(w, r, id, "", true)
		}
		return
	}
	if len(parts) == 2 && parts[1] == "openid-unlink" && r.Method == http.MethodPatch {
		if !isAdmin(current) {
			sendStatus(w, 403)
			return
		}
		u, err := h.userByID(r.Context(), id)
		if errors.Is(err, sql.ErrNoRows) {
			sendStatus(w, 404)
			return
		}
		if err != nil {
			http.Error(w, "database read failed", 500)
			return
		}
		if u.Type == "root" && current.Type != "root" {
			sendStatus(w, 403)
			return
		}
		var extra map[string]any
		_ = json.Unmarshal(u.ExtraData, &extra)
		if extra == nil || extra["authOpenIDSub"] == nil {
			sendStatus(w, 200)
			return
		}
		extra["authOpenIDSub"] = nil
		encoded, _ := json.Marshal(extra)
		if _, err := h.db.ExecContext(r.Context(), `UPDATE users SET extraData=?,updatedAt=? WHERE id=?`, encoded, dbtime.Now(), id); err != nil {
			http.Error(w, "database write failed", 500)
			return
		}
		u.ExtraData = encoded
		serialized, err := h.browserUserDB(r.Context(), u, []any{}, jsonArray(u.Bookmarks))
		if err != nil {
			http.Error(w, "database read failed", 500)
			return
		}
		if h.emit != nil {
			h.emit(current.ID, "user_updated", serialized)
		}
		sendStatus(w, 200)
		return
	}
	if len(parts) != 1 {
		return
	}
	if r.Method == http.MethodGet {
		if !isAdmin(current) {
			sendStatus(w, 403)
			return
		}
		u, err := h.userByID(r.Context(), id)
		if errors.Is(err, sql.ErrNoRows) {
			sendStatus(w, 404)
			return
		}
		if err != nil {
			http.Error(w, "database read failed", 500)
			return
		}
		progress, err := h.progressRows(r.Context(), id)
		if err != nil {
			http.Error(w, "database read failed", 500)
			return
		}
		serialized, err := h.browserUserDB(r.Context(), u, progress, jsonArray(u.Bookmarks))
		if err != nil {
			http.Error(w, "database read failed", 500)
			return
		}
		writeJSON(w, 200, serialized)
		return
	}
	if r.Method == http.MethodDelete {
		if !isAdmin(current) {
			sendStatus(w, 403)
			return
		}
		u, err := h.userByID(r.Context(), id)
		if errors.Is(err, sql.ErrNoRows) {
			sendStatus(w, 404)
			return
		}
		if err != nil {
			http.Error(w, "database read failed", 500)
			return
		}
		if current.ID == id {
			sendStatus(w, 400)
			return
		}
		if u.Type == "root" {
			sendStatus(w, 403)
			return
		}
		tx, err := h.db.BeginTx(r.Context(), nil)
		if err != nil {
			http.Error(w, "database write failed", 500)
			return
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(r.Context(), `DELETE FROM playlists WHERE userId = ?`, id); err != nil {
			http.Error(w, "database write failed", 500)
			return
		}
		if _, err = tx.ExecContext(r.Context(), `UPDATE playbackSessions SET userId = NULL WHERE userId = ?`, id); err != nil {
			http.Error(w, "database write failed", 500)
			return
		}
		if _, err = tx.ExecContext(r.Context(), `DELETE FROM users WHERE id = ?`, id); err != nil {
			http.Error(w, "database write failed", 500)
			return
		}
		if err = tx.Commit(); err != nil {
			http.Error(w, "database write failed", 500)
			return
		}
		if h.emit != nil {
			h.emit("admin", "user_removed", browserUser(u, []any{}, jsonArray(u.Bookmarks)))
		}
		writeJSON(w, 200, map[string]any{"success": true})
		return
	}
	if r.Method == http.MethodPatch {
		if !isAdmin(current) {
			sendStatus(w, 403)
			return
		}
		u, err := h.userByID(r.Context(), id)
		if errors.Is(err, sql.ErrNoRows) {
			sendStatus(w, 404)
			return
		}
		if err != nil {
			http.Error(w, "database read failed", 500)
			return
		}
		if u.Type == "root" && current.Type != "root" {
			sendStatus(w, 403)
			return
		}
		body, err := decodeBody(r)
		if err != nil {
			http.Error(w, "Invalid request", 400)
			return
		}
		for _, key := range []string{"id", "pash", "token", "extraData", "bookmarks"} {
			if _, ok := body[key]; ok {
				http.Error(w, `Key "`+key+`" cannot be updated`, 400)
				return
			}
		}
		if value, exists := body["email"]; exists && value != nil && value != "" {
			if _, ok := value.(string); !ok {
				sendText(w, 400, "Invalid email")
				return
			}
		}
		if value, exists := body["username"]; exists && value != nil && value != "" {
			if _, ok := value.(string); !ok {
				sendText(w, 400, "Invalid username")
				return
			}
		}
		if value, exists := body["type"]; exists && value != nil && value != "" {
			if _, ok := value.(string); !ok {
				sendText(w, 400, "Invalid account type")
				return
			}
		}
		if value, exists := body["permissions"]; exists && value != nil {
			if _, ok := value.(map[string]any); !ok {
				sendText(w, 400, "Invalid permissions")
				return
			}
		}
		credentialsChanged := false
		emailChanged := false
		if name, ok := body["username"].(string); ok && name != "" && name != u.Username {
			var exists int
			if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM users WHERE username=? AND id<>?`, name, id).Scan(&exists); err != nil {
				http.Error(w, "database read failed", 500)
				return
			}
			if exists != 0 {
				sendText(w, 400, "Username already taken")
				return
			}
			u.Username = name
			token, err := h.signToken(map[string]any{"userId": u.ID, "username": u.Username})
			if err != nil {
				http.Error(w, "database write failed", 500)
				return
			}
			u.Token = token
			credentialsChanged = true
		}
		if password, ok := body["password"].(string); ok && password != "" {
			hash, err := bcrypt.GenerateFromPassword([]byte(password), 8)
			if err != nil {
				http.Error(w, "database write failed", 500)
				return
			}
			u.Password = string(hash)
			credentialsChanged = true
		}
		if email, ok := body["email"].(string); ok && email != "" {
			u.Email = email
			emailChanged = true
		} else if email, exists := body["email"]; exists && (email == nil || email == "") {
			// Upstream PATCH with null/empty email updates the column; both
			// serialize as null in the browser DTO.
			u.Email = ""
			emailChanged = true
		}
		if typ, ok := body["type"].(string); ok && typ != "" && u.Type != "root" {
			if typ != "user" && typ != "admin" && typ != "guest" {
				sendText(w, 400, "Invalid account type")
				return
			}
			u.Type = typ
		}
		if active, ok := body["isActive"].(bool); ok {
			u.IsActive = active
		}
		perms := permissions(u)
		if patch, ok := body["permissions"].(map[string]any); ok {
			defaults := defaultPermissions(u.Type)
			for key, value := range patch {
				if _, allowed := defaults[key]; allowed {
					if b, ok := value.(bool); ok {
						perms[key] = b
					}
				}
			}
			for _, key := range []string{"librariesAccessible", "itemTagsSelected"} {
				if val, ok := patch[key]; ok && validStringArray(val) {
					perms[key] = val
				}
			}
		}
		for _, key := range []string{"librariesAccessible", "itemTagsSelected"} {
			if val, ok := body[key]; ok && validStringArray(val) {
				perms[key] = val
			}
		}
		encoded, _ := json.Marshal(perms)
		now := dbtime.Now()
		var writeErr error
		// The users row email must not be rewritten when the patch does not
		// touch it: an absent-email row would otherwise flip SQL NULL to '':
		// Sequelize update() only writes mutated attributes.
		if emailChanged {
			_, writeErr = h.db.ExecContext(r.Context(), `UPDATE users SET username=?,email=?,pash=?,token=?,type=?,isActive=?,permissions=?,updatedAt=? WHERE id=?`, u.Username, u.Email, u.Password, u.Token, u.Type, u.IsActive, encoded, now, u.ID)
		} else {
			_, writeErr = h.db.ExecContext(r.Context(), `UPDATE users SET username=?,pash=?,token=?,type=?,isActive=?,permissions=?,updatedAt=? WHERE id=?`, u.Username, u.Password, u.Token, u.Type, u.IsActive, encoded, now, u.ID)
		}
		if writeErr != nil {
			http.Error(w, "database write failed", 500)
			return
		}
		if credentialsChanged && h.invalidateSessions != nil {
			_, refresh, err := h.invalidateSessions(r.Context(), u.ID, refreshTokenFromRequest(r))
			if err != nil {
				http.Error(w, "database write failed", 500)
				return
			}
			if refresh != "" && r.Header.Get("x-refresh-token") == "" && h.setRefreshCookie != nil {
				h.setRefreshCookie(w, r, refresh)
			}
		}
		u.Permissions = encoded
		serialized, err := h.browserUserDB(r.Context(), u, []any{}, jsonArray(u.Bookmarks))
		if err != nil {
			http.Error(w, "database read failed", 500)
			return
		}
		if h.emit != nil {
			h.emit(current.ID, "user_updated", serialized)
		}
		writeJSON(w, 200, map[string]any{"success": true, "user": serialized})
		return
	}
	writeNotImplemented(w)
}

func (h *Handler) handleAPIKeys(w http.ResponseWriter, r *http.Request, current store.User, tail string) {
	if !isAdmin(current) {
		sendStatus(w, 403)
		return
	}
	id := strings.TrimPrefix(tail, "/")
	if id == "" && r.Method == http.MethodGet {
		keys, err := h.apiKeys(r.Context())
		if err != nil {
			http.Error(w, "database read failed", 500)
			return
		}
		writeJSON(w, 200, map[string]any{"apiKeys": keys})
		return
	}
	if id == "" && r.Method == http.MethodPost {
		body, err := decodeBody(r)
		if err != nil {
			sendStatus(w, 400)
			return
		}
		name, _ := body["name"].(string)
		userID, _ := body["userId"].(string)
		if name == "" || userID == "" {
			sendStatus(w, 400)
			return
		}
		target, err := h.userByID(r.Context(), userID)
		if errors.Is(err, sql.ErrNoRows) {
			sendStatus(w, 400)
			return
		}
		if err != nil {
			http.Error(w, "database read failed", 500)
			return
		}
		if target.Type == "root" && current.Type != "root" {
			sendStatus(w, 403)
			return
		}
		expiresIn := number(body["expiresIn"], 0)
		if body["expiresIn"] != nil && (expiresIn <= 0) {
			sendStatus(w, 400)
			return
		}
		keyID := newUUID()
		now := time.Now().UTC()
		var expires any
		if expiresIn > 0 {
			expires = dbtime.Format(now.Add(time.Duration(expiresIn * float64(time.Second))))
		}
		tokenClaims := map[string]any{"keyId": keyID, "name": name, "type": "api"}
		if expiresIn > 0 {
			tokenClaims["exp"] = now.Unix() + int64(expiresIn)
		}
		token, err := h.signToken(tokenClaims)
		if err != nil {
			http.Error(w, "API key creation failed", 500)
			return
		}
		active, _ := body["isActive"].(bool)
		if _, err = h.db.ExecContext(r.Context(), `INSERT INTO apiKeys (id,name,description,expiresAt,lastUsedAt,isActive,permissions,userId,createdByUserId,createdAt,updatedAt) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, keyID, name, nil, expires, nil, active, nil, userID, current.ID, dbtime.Format(now), dbtime.Format(now)); err != nil {
			http.Error(w, "API key creation failed", 500)
			return
		}
		entry, err := h.apiKey(r.Context(), keyID)
		if err != nil {
			http.Error(w, "database read failed", 500)
			return
		}
		// Upstream create spreads apiKey.toJSON() from a freshly built instance
		// (ApiKeyController.js:100): unpersisted attributes (description,
		// lastUsedAt, permissions) are undefined and dropped; createdByUserId
		// and user remain. The plain instance JSON has no createdByUser field.
		delete(entry, "description")
		delete(entry, "lastUsedAt")
		delete(entry, "permissions")
		delete(entry, "createdByUser")
		entry["apiKey"] = token
		writeJSON(w, 200, map[string]any{"apiKey": entry})
		return
	}
	if id == "" {
		sendStatus(w, 404)
		return
	}
	if r.Method == http.MethodDelete {
		res, err := h.db.ExecContext(r.Context(), `DELETE FROM apiKeys WHERE id=?`, id)
		if err != nil {
			http.Error(w, "database write failed", 500)
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			sendStatus(w, 404)
			return
		}
		sendStatus(w, 200)
		return
	}
	if r.Method == http.MethodPatch {
		var ownerID, ownerType string
		err := h.db.QueryRowContext(r.Context(), `SELECT u.id,u.type FROM apiKeys a JOIN users u ON u.id=a.userId WHERE a.id=?`, id).Scan(&ownerID, &ownerType)
		if errors.Is(err, sql.ErrNoRows) {
			sendStatus(w, 404)
			return
		}
		if err != nil {
			http.Error(w, "database read failed", 500)
			return
		}
		if ownerType == "root" && current.Type != "root" {
			sendStatus(w, 403)
			return
		}
		body, err := decodeBody(r)
		if err != nil {
			sendStatus(w, 400)
			return
		}
		var newOwner any = ownerID
		var active any
		setActive := false
		if val, ok := body["userId"]; ok {
			newID, ok := val.(string)
			if !ok {
				sendStatus(w, 400)
				return
			}
			target, e := h.userByID(r.Context(), newID)
			if e != nil {
				sendStatus(w, 400)
				return
			}
			if target.Type == "root" && current.Type != "root" {
				sendStatus(w, 403)
				return
			}
			newOwner = newID
		}
		if val, ok := body["isActive"]; ok {
			b, ok := val.(bool)
			if !ok {
				sendStatus(w, 400)
				return
			}
			active = b
			setActive = true
		}
		if setActive {
			_, err = h.db.ExecContext(r.Context(), `UPDATE apiKeys SET userId=?,isActive=?,updatedAt=? WHERE id=?`, newOwner, active, dbtime.Now(), id)
		} else {
			_, err = h.db.ExecContext(r.Context(), `UPDATE apiKeys SET userId=?,updatedAt=? WHERE id=?`, newOwner, dbtime.Now(), id)
		}
		if err != nil {
			http.Error(w, "database write failed", 500)
			return
		}
		key, err := h.apiKey(r.Context(), id)
		if err != nil {
			http.Error(w, "database read failed", 500)
			return
		}
		// Upstream update spreads apiKey.toJSON() from the instance it mutated
		// (ApiKeyController.js:~100): the plain instance JSON has no
		// createdByUser field, and lastUsedAt is the value as-loaded (the auth
		// usage that happened between operations does not refresh it).
		delete(key, "createdByUser")
		writeJSON(w, 200, map[string]any{"apiKey": key})
		return
	}
	sendStatus(w, 404)
}

var stripMarkup = regexp.MustCompile(`<[^>]*>`)

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func jsTruthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case float64:
		return v != 0
	case int:
		return v != 0
	case int64:
		return v != 0
	default:
		return true
	}
}

func cleanName(value string) string {
	return html.UnescapeString(stripMarkup.ReplaceAllString(value, ""))
}

func (h *Handler) collectionPlaylist(w http.ResponseWriter, r *http.Request, user store.User, path string) bool {
	if strings.HasPrefix(path, "/collections") {
		return h.collectionRoute(w, r, user, strings.TrimPrefix(path, "/collections"))
	}
	if strings.HasPrefix(path, "/playlists") {
		return h.playlistRoute(w, r, user, strings.TrimPrefix(path, "/playlists"))
	}
	return false
}

func (h *Handler) collectionRoute(w http.ResponseWriter, r *http.Request, user store.User, tail string) bool {
	if tail == "" || tail == "/" {
		switch r.Method {
		case http.MethodGet:
			rows, err := h.db.QueryContext(r.Context(), `SELECT id FROM collections ORDER BY createdAt ASC`)
			if err != nil {
				http.Error(w, "database read failed", 500)
				return true
			}
			// One-connection pool: materialize ids and close this cursor before
			// collectionDTO opens its own queries.
			ids := make([]string, 0)
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					http.Error(w, "database read failed", 500)
					return true
				}
				ids = append(ids, id)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				http.Error(w, "database read failed", 500)
				return true
			}
			rows.Close()
			out := make([]any, 0)
			for _, id := range ids {
				dto, libraryID, hasVisible, err := h.collectionDTO(r.Context(), id, user)
				if errors.Is(err, sql.ErrNoRows) {
					continue
				}
				if err != nil {
					http.Error(w, "database read failed", 500)
					return true
				}
				if canAccessLibrary(permissions(user), libraryID) && hasVisible {
					out = append(out, dto)
				}
			}
			writeJSON(w, 200, map[string]any{"collections": out})
			return true
		case http.MethodPost:
			h.createCollection(w, r, user)
			return true
		default:
			return false
		}
	}
	parts := strings.Split(strings.TrimPrefix(tail, "/"), "/")
	if len(parts) == 1 {
		id := parts[0]
		switch r.Method {
		case http.MethodGet:
			dto, libraryID, hasVisible, err := h.collectionDTO(r.Context(), id, user)
			if errors.Is(err, sql.ErrNoRows) || (!canAccessLibrary(permissions(user), libraryID) && err == nil) || !hasVisible {
				sendEntityNotFound(w, "Collection not found")
				return true
			}
			if err != nil {
				http.Error(w, "database read failed", 500)
				return true
			}
			if strings.Contains(","+r.URL.Query().Get("include")+",", ",rssfeed,") {
				feed, err := h.collectionRSSFeed(r, id)
				if err != nil {
					http.Error(w, "database read failed", 500)
					return true
				}
				if feed != nil {
					dto["rssFeed"] = feed
				}
			}
			writeJSON(w, 200, dto)
			return true
		case http.MethodPatch:
			return h.updateCollection(w, r, user, id)
		case http.MethodDelete:
			var libraryID string
			if err := h.db.QueryRowContext(r.Context(), `SELECT libraryId FROM collections WHERE id=?`, id).Scan(&libraryID); errors.Is(err, sql.ErrNoRows) {
				sendEntityNotFound(w, "Collection not found")
				return true
			} else if err != nil {
				http.Error(w, "database read failed", 500)
				return true
			}
			if !canAccessLibrary(permissions(user), libraryID) {
				sendEntityNotFound(w, "Collection not found")
				return true
			}
			if !boolValue(permissions(user)["delete"]) {
				sendStatus(w, 403)
				return true
			}
			dto, _, _, _ := h.collectionDTO(r.Context(), id, user)
			if _, err := h.db.ExecContext(r.Context(), `DELETE FROM collections WHERE id=?`, id); err != nil {
				http.Error(w, "database write failed", 500)
				return true
			}
			if h.emit != nil {
				h.emit("*", "collection_removed", dto)
			}
			sendStatus(w, 200)
			return true
		}
	}
	if len(parts) == 2 && parts[1] == "book" && r.Method == http.MethodPost {
		return h.addCollectionBook(w, r, user, parts[0])
	}
	if len(parts) == 3 && parts[1] == "book" && r.Method == http.MethodDelete {
		return h.removeCollectionBook(w, r, user, parts[0], parts[2])
	}
	if len(parts) == 3 && parts[1] == "batch" && r.Method == http.MethodPost {
		switch parts[2] {
		case "add":
			return h.batchCollectionBooks(w, r, user, parts[0], true)
		case "remove":
			return h.batchCollectionBooks(w, r, user, parts[0], false)
		}
	}
	return false
}

func (h *Handler) createCollection(w http.ResponseWriter, r *http.Request, user store.User) {
	if !boolValue(permissions(user)["update"]) {
		sendStatus(w, 403)
		return
	}
	body, err := decodeBody(r)
	if err != nil {
		sendText(w, 400, "Invalid collection data")
		return
	}
	name, _ := body["name"].(string)
	name = cleanName(name)
	libraryID, _ := body["libraryId"].(string)
	description, descOK := body["description"].(string)
	if name == "" || libraryID == "" {
		sendText(w, 400, "Invalid collection data")
		return
	}
	if jsTruthy(body["description"]) && !descOK {
		http.Error(w, "Invalid collection description", 400)
		return
	}
	// Collection.create persists `description || null` upstream; an empty
	// string therefore round-trips as JSON null, not as an empty string.
	storedDescription := collectionDescriptionForDB(description)
	if !canAccessLibrary(permissions(user), libraryID) {
		sendStatus(w, 403)
		return
	}
	bookIDs := stringSlice(body["books"])
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
	id := newUUID()
	now := dbtime.Now()
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "Failed to create collection", 500)
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO collections (id,name,description,libraryId,createdAt,updatedAt) VALUES (?,?,?,?,?,?)`, id, name, storedDescription, libraryID, now, now); err != nil {
		http.Error(w, "Failed to create collection", 500)
		return
	}
	for i, bookID := range mediaIDs {
		if _, err = tx.ExecContext(r.Context(), `INSERT INTO collectionBooks (id,collectionId,bookId,"order",createdAt) VALUES (?,?,?,?,?)`, newUUID(), id, bookID, i+1, now); err != nil {
			http.Error(w, "Failed to create collection", 500)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		http.Error(w, "Failed to create collection", 500)
		return
	}
	dto, _, _, err := h.collectionDTO(r.Context(), id, user)
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	if h.emit != nil {
		h.emit("*", "collection_added", dto)
	}
	writeJSON(w, 200, dto)
}

func collectionDescriptionForDB(description string) any {
	if description == "" {
		return nil
	}
	return description
}

func (h *Handler) collectionDTO(ctx context.Context, id string, user store.User) (map[string]any, string, bool, error) {
	var libraryID, name string
	var description sql.NullString
	var created, updated any
	err := h.db.QueryRowContext(ctx, `SELECT libraryId,name,description,createdAt,updatedAt FROM collections WHERE id=?`, id).Scan(&libraryID, &name, &description, &created, &updated)
	if err != nil {
		return nil, "", false, err
	}
	rows, err := h.db.QueryContext(ctx, `SELECT li.id FROM collectionBooks cb JOIN libraryItems li ON li.mediaId=cb.bookId AND li.mediaType='book' WHERE cb.collectionId=? ORDER BY cb."order" ASC`, id)
	if err != nil {
		return nil, "", false, err
	}
	// Materialize the item IDs before running per-item queries: the database
	// pool allows a single connection, and nested queries while these rows are
	// open would deadlock waiting for a free connection.
	itemIDs := make([]string, 0)
	for rows.Next() {
		var itemID string
		if err := rows.Scan(&itemID); err != nil {
			rows.Close()
			return nil, "", false, err
		}
		itemIDs = append(itemIDs, itemID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, "", false, err
	}
	rows.Close()
	books := make([]any, 0)
	visible := 0
	for _, itemID := range itemIDs {
		access, err := h.canAccessItem(ctx, user, itemID)
		if err != nil {
			return nil, "", false, err
		}
		if !access.allowed {
			continue
		}
		item, err := h.items.ItemJSON(ctx, itemID, true)
		if err != nil {
			return nil, "", false, err
		}
		books = append(books, item)
		visible++
	}
	return map[string]any{"id": id, "libraryId": libraryID, "name": name, "description": nullableValue(description), "books": books, "lastUpdate": millis(updated), "createdAt": millis(created)}, libraryID, visible > 0 || len(books) == 0, nil
}

func (h *Handler) updateCollection(w http.ResponseWriter, r *http.Request, user store.User, id string) bool {
	var libraryID, name string
	var desc sql.NullString
	if err := h.db.QueryRowContext(r.Context(), `SELECT libraryId,name,description FROM collections WHERE id=?`, id).Scan(&libraryID, &name, &desc); errors.Is(err, sql.ErrNoRows) {
		sendStatus(w, 404)
		return true
	} else if err != nil {
		http.Error(w, "database read failed", 500)
		return true
	}
	if !canAccessLibrary(permissions(user), libraryID) {
		sendStatus(w, 404)
		return true
	}
	if !boolValue(permissions(user)["update"]) {
		sendStatus(w, 403)
		return true
	}
	oldName, oldDesc := name, desc
	body, err := decodeBody(r)
	if err != nil {
		http.Error(w, "Invalid request", 400)
		return true
	}
	if val, ok := body["description"]; ok && val != nil {
		if s, ok := val.(string); ok {
			desc = sql.NullString{String: s, Valid: true}
		} else {
			http.Error(w, "database write failed", 500)
			return true
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
		http.Error(w, "database write failed", 500)
		return true
	}
	defer tx.Rollback()
	if books := stringSlice(body["books"]); len(books) > 0 {
		rows, err := tx.QueryContext(r.Context(), `SELECT cb.id,li.id,cb."order" FROM collectionBooks cb JOIN libraryItems li ON li.mediaId=cb.bookId AND li.mediaType='book' WHERE cb.collectionId=?`, id)
		if err != nil {
			http.Error(w, "database write failed", 500)
			return true
		}
		type collectionLink struct {
			linkID, itemID string
			order          int
		}
		links := make([]collectionLink, 0)
		for rows.Next() {
			var link collectionLink
			if err := rows.Scan(&link.linkID, &link.itemID, &link.order); err != nil {
				rows.Close()
				http.Error(w, "database write failed", 500)
				return true
			}
			links = append(links, link)
		}
		rows.Close()
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
				if _, err := tx.ExecContext(r.Context(), `UPDATE collectionBooks SET "order"=? WHERE id=?`, i+1, link.linkID); err != nil {
					http.Error(w, "database write failed", 500)
					return true
				}
				changed = true
			}
		}
	}
	if changed {
		if _, err := tx.ExecContext(r.Context(), `UPDATE collections SET name=?,description=?,updatedAt=? WHERE id=?`, name, desc, dbtime.Now(), id); err != nil {
			http.Error(w, "database write failed", 500)
			return true
		}
	}
	if err = tx.Commit(); err != nil {
		http.Error(w, "database write failed", 500)
		return true
	}
	dto, _, _, err := h.collectionDTO(r.Context(), id, user)
	if err != nil {
		http.Error(w, "database read failed", 500)
		return true
	}
	if changed && h.emit != nil {
		h.emit("*", "collection_updated", dto)
	}
	writeJSON(w, 200, dto)
	return true
}

// batchCollectionBooks mirrors the legacy batch endpoints. Invalid identifiers
// are filtered before the query; adding a batch with no valid books is an
// error, while removing unknown books is an idempotent no-op.
func (h *Handler) batchCollectionBooks(w http.ResponseWriter, r *http.Request, user store.User, id string, add bool) bool {
	var libraryID string
	if err := h.db.QueryRowContext(r.Context(), `SELECT libraryId FROM collections WHERE id=?`, id).Scan(&libraryID); errors.Is(err, sql.ErrNoRows) {
		sendEntityNotFound(w, "Collection not found")
		return true
	} else if err != nil {
		http.Error(w, "database read failed", 500)
		return true
	}
	if !canAccessLibrary(permissions(user), libraryID) {
		sendEntityNotFound(w, "Collection not found")
		return true
	}
	if !boolValue(permissions(user)["update"]) {
		sendStatus(w, 403)
		return true
	}
	body, err := decodeBody(r)
	if err != nil {
		if add {
			sendText(w, 400, "Invalid request body")
		} else {
			http.Error(w, "Invalid request body", 500)
		}
		return true
	}
	ids := validCollectionBookIDs(body["books"])
	if len(ids) == 0 {
		if add {
			sendText(w, 400, "Invalid request body")
		} else {
			http.Error(w, "Invalid request body", 500)
		}
		return true
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+2)
	for _, itemID := range ids {
		args = append(args, itemID)
	}
	query := `SELECT id,mediaId FROM libraryItems WHERE id IN (` + placeholders + `) AND mediaType='book'`
	if add {
		query += ` AND libraryId=?`
		args = append(args, libraryID)
	}
	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		http.Error(w, "database read failed", 500)
		return true
	}
	mediaByItem := make(map[string]string)
	mediaSet := make(map[string]bool)
	for rows.Next() {
		var itemID, mediaID string
		if err := rows.Scan(&itemID, &mediaID); err != nil {
			rows.Close()
			http.Error(w, "database read failed", 500)
			return true
		}
		mediaByItem[itemID] = mediaID
		mediaSet[mediaID] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		http.Error(w, "database read failed", 500)
		return true
	}
	rows.Close()
	if add && len(mediaByItem) == 0 {
		sendText(w, 400, "Invalid request body. No valid books")
		return true
	}
	if add {
		var order int
		if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM collectionBooks WHERE collectionId=?`, id).Scan(&order); err != nil {
			http.Error(w, "database read failed", 500)
			return true
		}
		rows, err := h.db.QueryContext(r.Context(), `SELECT bookId FROM collectionBooks WHERE collectionId=?`, id)
		if err != nil {
			http.Error(w, "database read failed", 500)
			return true
		}
		existing := map[string]bool{}
		for rows.Next() {
			var mediaID string
			if err := rows.Scan(&mediaID); err != nil {
				rows.Close()
				http.Error(w, "database read failed", 500)
				return true
			}
			existing[mediaID] = true
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			http.Error(w, "database read failed", 500)
			return true
		}
		rows.Close()
		tx, err := h.db.BeginTx(r.Context(), nil)
		if err != nil {
			http.Error(w, "database write failed", 500)
			return true
		}
		defer tx.Rollback()
		changed := false
		for _, itemID := range ids {
			mediaID := mediaByItem[itemID]
			if mediaID == "" || existing[mediaID] {
				continue
			}
			order++
			changed = true
			existing[mediaID] = true
			if _, err := tx.ExecContext(r.Context(), `INSERT INTO collectionBooks(id,collectionId,bookId,"order",createdAt) VALUES(?,?,?,?,?)`, newUUID(), id, mediaID, order, dbtime.Now()); err != nil {
				http.Error(w, "database write failed", 500)
				return true
			}
		}
		if err := tx.Commit(); err != nil {
			http.Error(w, "database write failed", 500)
			return true
		}
		return h.writeCollectionAfterBatch(w, r, user, id, changed)
	}

	rows, err = h.db.QueryContext(r.Context(), `SELECT id,bookId,"order" FROM collectionBooks WHERE collectionId=? ORDER BY "order" ASC`, id)
	if err != nil {
		http.Error(w, "database read failed", 500)
		return true
	}
	type link struct {
		id, mediaID string
		order       int
	}
	links := make([]link, 0)
	for rows.Next() {
		var entry link
		if err := rows.Scan(&entry.id, &entry.mediaID, &entry.order); err != nil {
			rows.Close()
			http.Error(w, "database read failed", 500)
			return true
		}
		links = append(links, entry)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		http.Error(w, "database read failed", 500)
		return true
	}
	rows.Close()
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "database write failed", 500)
		return true
	}
	defer tx.Rollback()
	changed, order := false, 1
	for _, entry := range links {
		if mediaSet[entry.mediaID] {
			if _, err := tx.ExecContext(r.Context(), `DELETE FROM collectionBooks WHERE id=?`, entry.id); err != nil {
				http.Error(w, "database write failed", 500)
				return true
			}
			changed = true
			continue
		}
		if entry.order != order {
			if _, err := tx.ExecContext(r.Context(), `UPDATE collectionBooks SET "order"=? WHERE id=?`, order, entry.id); err != nil {
				http.Error(w, "database write failed", 500)
				return true
			}
			changed = true
		}
		order++
	}
	if err := tx.Commit(); err != nil {
		http.Error(w, "database write failed", 500)
		return true
	}
	return h.writeCollectionAfterBatch(w, r, user, id, changed)
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

func (h *Handler) writeCollectionAfterBatch(w http.ResponseWriter, r *http.Request, user store.User, id string, changed bool) bool {
	dto, _, _, err := h.collectionDTO(r.Context(), id, user)
	if err != nil {
		http.Error(w, "database read failed", 500)
		return true
	}
	if changed && h.emit != nil {
		h.emit("*", "collection_updated", dto)
	}
	writeJSONKeys(w, 200, dto, []string{"id", "libraryId", "name", "userId", "description", "lastUpdate", "createdAt", "books"})
	return true
}

func (h *Handler) addCollectionBook(w http.ResponseWriter, r *http.Request, user store.User, id string) bool {
	var libraryID string
	err := h.db.QueryRowContext(r.Context(), `SELECT libraryId FROM collections WHERE id=?`, id).Scan(&libraryID)
	if errors.Is(err, sql.ErrNoRows) {
		sendEntityNotFound(w, "Collection not found")
		return true
	}
	if err != nil {
		http.Error(w, "database read failed", 500)
		return true
	}
	if !canAccessLibrary(permissions(user), libraryID) {
		sendEntityNotFound(w, "Collection not found")
		return true
	}
	if !boolValue(permissions(user)["update"]) {
		sendStatus(w, 403)
		return true
	}
	body, err := decodeBody(r)
	if err != nil {
		sendStatus(w, 400)
		return true
	}
	itemID, _ := body["id"].(string)
	var mediaID, mediaType, lib string
	err = h.db.QueryRowContext(r.Context(), `SELECT mediaId,mediaType,libraryId FROM libraryItems WHERE id=?`, itemID).Scan(&mediaID, &mediaType, &lib)
	if err != nil {
		http.Error(w, "Book not found", 404)
		return true
	}
	if lib != libraryID {
		http.Error(w, "Book in different library", 400)
		return true
	}
	if mediaType != "book" {
		http.Error(w, "Book not found", 404)
		return true
	}
	var count, exists int
	_ = h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM collectionBooks WHERE collectionId=?`, id).Scan(&count)
	_ = h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM collectionBooks WHERE collectionId=? AND bookId=?`, id, mediaID).Scan(&exists)
	if exists > 0 {
		http.Error(w, "Book already in collection", 400)
		return true
	}
	now := dbtime.Now()
	if _, err = h.db.ExecContext(r.Context(), `INSERT INTO collectionBooks(id,collectionId,bookId,"order",createdAt) VALUES(?,?,?,?,?)`, newUUID(), id, mediaID, count+1, now); err != nil {
		http.Error(w, "database write failed", 500)
		return true
	}
	dto, _, _, err := h.collectionDTO(r.Context(), id, user)
	if err != nil {
		http.Error(w, "database read failed", 500)
		return true
	}
	if h.emit != nil {
		h.emit("*", "collection_updated", dto)
	}
	writeJSON(w, 200, dto)
	return true
}

func (h *Handler) removeCollectionBook(w http.ResponseWriter, r *http.Request, user store.User, id, itemID string) bool {
	var libraryID string
	err := h.db.QueryRowContext(r.Context(), `SELECT libraryId FROM collections WHERE id=?`, id).Scan(&libraryID)
	if errors.Is(err, sql.ErrNoRows) {
		sendEntityNotFound(w, "Collection not found")
		return true
	}
	if err != nil {
		http.Error(w, "database read failed", 500)
		return true
	}
	if !canAccessLibrary(permissions(user), libraryID) {
		sendEntityNotFound(w, "Collection not found")
		return true
	}
	if !boolValue(permissions(user)["update"]) {
		sendStatus(w, 403)
		return true
	}
	var mediaID string
	err = h.db.QueryRowContext(r.Context(), `SELECT mediaId FROM libraryItems WHERE id=?`, itemID).Scan(&mediaID)
	if errors.Is(err, sql.ErrNoRows) {
		sendStatus(w, 404)
		return true
	}
	if err != nil {
		http.Error(w, "database read failed", 500)
		return true
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		http.Error(w, "database write failed", 500)
		return true
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(r.Context(), `DELETE FROM collectionBooks WHERE collectionId=? AND bookId=?`, id, mediaID)
	if err != nil {
		http.Error(w, "database write failed", 500)
		return true
	}
	removed, _ := res.RowsAffected()
	rows, err := tx.QueryContext(r.Context(), `SELECT id FROM collectionBooks WHERE collectionId=? ORDER BY "order"`, id)
	if err != nil {
		http.Error(w, "database write failed", 500)
		return true
	}
	seq := 1
	for rows.Next() {
		var link string
		_ = rows.Scan(&link)
		_, _ = tx.ExecContext(r.Context(), `UPDATE collectionBooks SET "order"=? WHERE id=?`, seq, link)
		seq++
	}
	rows.Close()
	if err = tx.Commit(); err != nil {
		http.Error(w, "database write failed", 500)
		return true
	}
	dto, _, _, err := h.collectionDTO(r.Context(), id, user)
	if err != nil {
		http.Error(w, "database read failed", 500)
		return true
	}
	if removed > 0 && h.emit != nil {
		h.emit("*", "collection_updated", dto)
	}
	writeJSON(w, 200, dto)
	return true
}

func (h *Handler) apiKeys(ctx context.Context) ([]map[string]any, error) {
	rows, err := h.db.QueryContext(ctx, `SELECT id FROM apiKeys`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	// The pool uses one connection; nested queries must run only after the
	// outer cursor closes (see internal/scanner watchedLibraries comment).
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		entry, err := h.apiKey(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	return out, nil
}
func (h *Handler) apiKey(ctx context.Context, id string) (map[string]any, error) {
	var name, description sql.NullString
	var expires, lastUsed, created, updated any
	var active int
	var permissionsRaw []byte
	var userID, creatorID sql.NullString
	err := h.db.QueryRowContext(ctx, `SELECT name,description,expiresAt,lastUsedAt,isActive,permissions,userId,createdByUserId,createdAt,updatedAt FROM apiKeys WHERE id=?`, id).Scan(&name, &description, &expires, &lastUsed, &active, &permissionsRaw, &userID, &creatorID, &created, &updated)
	if err != nil {
		return nil, err
	}
	var perms any
	if len(permissionsRaw) > 0 {
		_ = json.Unmarshal(permissionsRaw, &perms)
	}
	user := map[string]any(nil)
	creator := map[string]any(nil)
	if userID.Valid {
		if u, e := h.userByID(ctx, userID.String); e == nil {
			user = map[string]any{"id": u.ID, "username": u.Username, "type": u.Type}
		}
	}
	if creatorID.Valid {
		if u, e := h.userByID(ctx, creatorID.String); e == nil {
			creator = map[string]any{"id": u.ID, "username": u.Username, "type": u.Type}
		}
	}
	return map[string]any{"id": id, "name": name.String, "description": nullableValue(description), "expiresAt": sequelizeISOString(expires), "lastUsedAt": sequelizeISOString(lastUsed), "isActive": active != 0, "permissions": perms, "userId": nullableValue(userID), "createdByUserId": nullableValue(creatorID), "createdAt": millis(created), "updatedAt": millis(updated), "user": user, "createdByUser": creator}, nil
}

// sequelizeISOString mirrors Sequelize model serialization of DATE attributes
// (apiKey.toJSON() in ApiKeyController.js): ISO-8601 UTC strings with
// millisecond precision, or null when the attribute is null.
func sequelizeISOString(v any) any {
	var t time.Time
	switch value := v.(type) {
	case nil:
		return nil
	case time.Time:
		t = value
	case int64:
		t = time.UnixMilli(value)
	case float64:
		t = time.UnixMilli(int64(value))
	case []byte:
		return sequelizeISOString(parseDateBytes(value))
	case string:
		return sequelizeISOString(parseDateString(value))
	default:
		return nil
	}
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// parseDateBytes/parseDateString decode the SQLite driver DATETIME formats the
// identity/userapi layer already handles elsewhere.
func parseDateBytes(raw []byte) time.Time {
	if len(raw) == 0 {
		return time.Time{}
	}
	return parseDateString(string(raw))
}

func parseDateString(value string) time.Time {
	for _, layout := range []string{"2006-01-02T15:04:05.000Z", "2006-01-02T15:04:05Z", "2006-01-02 15:04:05.000 -07:00", "2006-01-02 15:04:05 -07:00", "2006-01-02T15:04:05.999999999"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t
		}
	}
	return time.Time{}
}

func (h *Handler) signToken(payload map[string]any) (string, error) {
	// jsonwebtoken's default iat is the current Unix time in seconds.
	payload["iat"] = time.Now().Unix()
	secret := []byte(h.cfg.JWTSecret)
	if len(secret) == 0 {
		var raw []byte
		if err := h.db.QueryRow(`SELECT value FROM settings WHERE key='server-settings'`).Scan(&raw); err != nil {
			return "", err
		}
		var settings map[string]any
		if err := json.Unmarshal(raw, &settings); err != nil {
			return "", err
		}
		secret, _ = settings["tokenSecret"].([]byte)
		if secret == nil {
			if s, ok := settings["tokenSecret"].(string); ok {
				secret = []byte(s)
			}
		}
	}
	if len(secret) == 0 {
		return "", errors.New("missing token secret")
	}
	enc := base64.RawURLEncoding.EncodeToString
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	unsigned := enc(header) + "." + enc(body)
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(unsigned))
	return unsigned + "." + enc(mac.Sum(nil)), nil
}

type itemAccess struct{ exists, allowed bool }

func (h *Handler) canAccessItem(ctx context.Context, user store.User, itemID string) (itemAccess, error) {
	var libraryID, mediaType string
	var explicit int
	var rawTags []byte
	err := h.db.QueryRowContext(ctx, `SELECT li.libraryId, li.mediaType, COALESCE(b.explicit,p.explicit,0), COALESCE(b.tags,p.tags,'[]') FROM libraryItems li LEFT JOIN books b ON li.mediaType='book' AND b.id=li.mediaId LEFT JOIN podcasts p ON li.mediaType='podcast' AND p.id=li.mediaId WHERE li.id=?`, itemID).Scan(&libraryID, &mediaType, &explicit, &rawTags)
	if errors.Is(err, sql.ErrNoRows) {
		return itemAccess{}, nil
	}
	if err != nil {
		return itemAccess{}, err
	}
	perms := permissions(user)
	if !canAccessLibrary(perms, libraryID) {
		return itemAccess{exists: true}, nil
	}
	if explicit != 0 && !boolValue(perms["accessExplicitContent"]) {
		return itemAccess{exists: true}, nil
	}
	var tags []string
	_ = json.Unmarshal(rawTags, &tags)
	return itemAccess{exists: true, allowed: canAccessTags(perms, tags)}, nil
}

func (h *Handler) progressRows(ctx context.Context, userID string) ([]any, error) {
	return ProgressRows(ctx, h.db, userID)
}

// ProgressRows returns the legacy DTOs for a user's media progress.
func ProgressRows(ctx context.Context, db *sql.DB, userID string) ([]any, error) {
	return progressdto.Read(ctx, db, userID)
}

// BrowserUser serializes a user using the legacy browser shape with progress
// and bookmarks supplied by the caller.
func BrowserUser(user store.User, progress []any, bookmarks []any) map[string]any {
	return browserUser(user, progress, bookmarks)
}

func (h *Handler) userPayload(ctx context.Context, user store.User) (map[string]any, error) {
	progress, err := h.progressRows(ctx, user.ID)
	if err != nil {
		return nil, err
	}
	return h.browserUserDB(ctx, user, progress, jsonArray(user.Bookmarks))
}

func (h *Handler) browserUserDB(ctx context.Context, user store.User, progress, bookmarks []any) (map[string]any, error) {
	result := browserUser(user, progress, bookmarks)
	var email sql.NullString
	if err := h.db.QueryRowContext(ctx, `SELECT email FROM users WHERE id=?`, user.ID).Scan(&email); err != nil {
		return nil, err
	}
	result["email"] = nullableValue(email)
	return result, nil
}

func (h *Handler) userByID(ctx context.Context, id string) (store.User, error) {
	return scanUser(h.db.QueryRowContext(ctx, `SELECT id,username,email,pash,type,token,isActive,isLocked,lastSeen,permissions,bookmarks,extraData,createdAt,updatedAt FROM users WHERE id=?`, id))
}

func scanUser(row rowScanner) (store.User, error) {
	var u store.User
	var email, pash, typ, token sql.NullString
	var active, locked int
	var lastSeen, createdAt, updatedAt any
	var permissionsRaw, bookmarksRaw, extraRaw []byte
	if err := row.Scan(&u.ID, &u.Username, &email, &pash, &typ, &token, &active, &locked, &lastSeen, &permissionsRaw, &bookmarksRaw, &extraRaw, &createdAt, &updatedAt); err != nil {
		return store.User{}, err
	}
	u.Email, u.Password, u.Type, u.Token = email.String, pash.String, typ.String, token.String
	u.IsActive, u.IsLocked = active != 0, locked != 0
	u.LastSeen, u.CreatedAt, u.UpdatedAt = int64Pointer(millis(lastSeen)), int64Pointer(millis(createdAt)), int64Pointer(millis(updatedAt))
	u.Permissions, u.Bookmarks, u.ExtraData = permissionsRaw, bookmarksRaw, extraRaw
	return u, nil
}

func int64Pointer(value any) *int64 {
	n, ok := value.(int64)
	if !ok {
		return nil
	}
	return &n
}
func isAdmin(user store.User) bool { return user.Type == "root" || user.Type == "admin" }

func minimalBrowserUser(user store.User, hideRootToken bool) map[string]any {
	permissionsMap := permissions(user)
	libraries, tags := stringSlice(permissionsMap["librariesAccessible"]), stringSlice(permissionsMap["itemTagsSelected"])
	if libraries == nil {
		libraries = []string{}
	}
	if tags == nil {
		tags = []string{}
	}
	delete(permissionsMap, "librariesAccessible")
	delete(permissionsMap, "itemTagsSelected")
	var extra map[string]any
	_ = json.Unmarshal(user.ExtraData, &extra)
	series := any([]any{})
	if extra != nil && extra["seriesHideFromContinueListening"] != nil {
		series = extra["seriesHideFromContinueListening"]
	}
	token := user.Token
	if user.Type == "root" && hideRootToken {
		token = ""
	}
	var lastSeen, createdAt any
	if user.LastSeen != nil {
		lastSeen = *user.LastSeen
	}
	if user.CreatedAt != nil {
		createdAt = *user.CreatedAt
	}
	return map[string]any{"id": user.ID, "username": user.Username, "email": nullableUserEmail(user.Email), "type": user.Type, "token": token, "seriesHideFromContinueListening": series, "isActive": user.IsActive, "isLocked": user.IsLocked, "lastSeen": lastSeen, "createdAt": createdAt, "permissions": permissionsMap, "librariesAccessible": libraries, "itemTagsSelected": tags, "hasOpenIDLink": extra != nil && extra["authOpenIDSub"] != nil}
}

func nullableUserEmail(email string) any {
	if email == "" {
		return nil
	}
	return email
}

func defaultPermissions(userType string) map[string]any {
	return map[string]any{"download": true, "update": userType == "root" || userType == "admin", "delete": userType == "root", "upload": userType == "root" || userType == "admin", "createEreader": userType == "root" || userType == "admin", "accessAllLibraries": true, "accessAllTags": true, "accessExplicitContent": userType == "root" || userType == "admin", "selectedTagsNotAccessible": false, "librariesAccessible": []string{}, "itemTagsSelected": []string{}}
}

func validStringArray(value any) bool {
	switch a := value.(type) {
	case []any:
		for _, v := range a {
			if _, ok := v.(string); !ok {
				return false
			}
		}
		return true
	case []string:
		return true
	default:
		return false
	}
}
func writeNotImplemented(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "This route is not implemented in the experimental Go backend"})
}

func (h *Handler) emitUserUpdate(ctx context.Context, user store.User) {
	if h.emit == nil {
		return
	}
	payload, err := h.userPayload(ctx, user)
	if err == nil {
		h.emit(user.ID, "user_updated", payload)
	}
}

type rowScanner interface{ Scan(...any) error }

func scanProgress(row rowScanner) (map[string]any, error) {
	var id, userID, mediaItemID, mediaType string
	var duration, current, ebook sql.NullFloat64
	var finished, hide int
	var ebookLocation sql.NullString
	var finishedAt, createdAt, updatedAt any
	var extraRaw []byte
	if err := row.Scan(&id, &userID, &mediaItemID, &mediaType, &duration, &current, &finished, &hide, &ebookLocation, &ebook, &finishedAt, &extraRaw, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	var extra map[string]any
	_ = json.Unmarshal(extraRaw, &extra)
	var libID any
	if extra != nil {
		libID = extra["libraryItemId"]
	}
	var epID any
	if mediaType == "podcastEpisode" {
		epID = mediaItemID
	}
	var progress any
	if extra != nil {
		progress = extra["progress"]
	}
	if progress == nil {
		progress = 0
	}
	return map[string]any{"id": id, "userId": userID, "libraryItemId": libID, "episodeId": epID, "mediaItemId": mediaItemID, "mediaItemType": mediaType, "duration": nullNumber(duration), "progress": progress, "currentTime": nullNumber(current), "isFinished": finished != 0, "hideFromContinueListening": hide != 0, "ebookLocation": nullableValue(ebookLocation), "ebookProgress": nullNumber(ebook), "lastUpdate": millis(updatedAt), "startedAt": millis(createdAt), "finishedAt": nullableMillis(finishedAt)}, nil
}

func browserUser(user store.User, progress []any, bookmarks []any) map[string]any {
	var permissionsMap map[string]any
	_ = json.Unmarshal(user.Permissions, &permissionsMap)
	if permissionsMap == nil {
		permissionsMap = map[string]any{}
	}
	// User.js:604-606/629-630: librariesAccessible and itemTagsSelected are
	// always serialized as arrays.
	libraries := stringSlice(permissionsMap["librariesAccessible"])
	tags := stringSlice(permissionsMap["itemTagsSelected"])
	if libraries == nil {
		libraries = []string{}
	}
	if tags == nil {
		tags = []string{}
	}
	delete(permissionsMap, "librariesAccessible")
	delete(permissionsMap, "itemTagsSelected")
	var extra map[string]any
	_ = json.Unmarshal(user.ExtraData, &extra)
	series := any([]any{})
	if extra != nil && extra["seriesHideFromContinueListening"] != nil {
		series = extra["seriesHideFromContinueListening"]
	}
	return map[string]any{"id": user.ID, "username": user.Username, "email": nullableUserEmail(user.Email), "type": user.Type, "token": user.Token, "mediaProgress": progress, "seriesHideFromContinueListening": series, "bookmarks": bookmarks, "isActive": user.IsActive, "isLocked": user.IsLocked, "lastSeen": user.LastSeen, "createdAt": user.CreatedAt, "permissions": permissionsMap, "librariesAccessible": libraries, "itemTagsSelected": tags, "hasOpenIDLink": extra != nil && extra["authOpenIDSub"] != nil && extra["authOpenIDSub"] != ""}
}

func jsonArray(raw json.RawMessage) []any {
	var out []any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	if out == nil {
		return []any{}
	}
	return out
}
func permissions(user store.User) map[string]any {
	var out map[string]any
	_ = json.Unmarshal(user.Permissions, &out)
	if out == nil {
		out = map[string]any{}
	}
	return out
}
func canAccessLibrary(p map[string]any, id string) bool {
	if boolValue(p["accessAllLibraries"]) {
		return true
	}
	for _, lib := range stringSlice(p["librariesAccessible"]) {
		if lib == id {
			return true
		}
	}
	return false
}
func canAccessTags(p map[string]any, tags []string) bool {
	if boolValue(p["accessAllTags"]) {
		return true
	}
	selected := stringSlice(p["itemTagsSelected"])
	inverted := boolValue(p["selectedTagsNotAccessible"])
	if len(tags) == 0 {
		return inverted
	}
	if inverted {
		for _, tag := range tags {
			for _, sel := range selected {
				if tag == sel {
					return false
				}
			}
		}
		return true
	}
	for _, tag := range tags {
		for _, sel := range selected {
			if tag == sel {
				return true
			}
		}
	}
	return false
}
func stringSlice(v any) []string {
	var out []string
	switch a := v.(type) {
	case []string:
		return a
	case []any:
		for _, x := range a {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
func boolValue(v any) bool { b, _ := v.(bool); return b }
func jsonNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case json.Number:
		x, e := n.Float64()
		return x, e == nil
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}
func number(v any, fallback float64) float64 {
	if n, ok := jsonNumber(v); ok && !math.IsNaN(n) && !math.IsInf(n, 0) {
		return n
	}
	return fallback
}
func finiteNumber(v any) (float64, bool) {
	n, ok := jsonNumber(v)
	return n, ok && !math.IsNaN(n) && !math.IsInf(n, 0)
}
func nullNumber(n sql.NullFloat64) any {
	if !n.Valid {
		return nil
	}
	return n.Float64
}
func nullableValue(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	return s.String
}
func millis(v any) any {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case time.Time:
		return t.UnixMilli()
	case int64:
		return t
	case float64:
		return int64(t)
	case []byte:
		return parseDateMillis(string(t))
	case string:
		return parseDateMillis(t)
	}
	return nil
}
func nullableMillis(v any) any { return millis(v) }
func parseDateMillis(v string) any {
	if n, e := strconv.ParseInt(v, 10, 64); e == nil {
		return n
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999 -07:00", // Sequelize SQLite DATETIME
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
	} {
		if t, e := time.Parse(layout, v); e == nil {
			return t.UnixMilli()
		}
	}
	return nil
}
func timestampString(v any, fallback string) any {
	if n, ok := finiteNumber(v); ok {
		return dbtime.Format(time.UnixMilli(int64(n)).UTC())
	}
	return fallback
}
func nullableString(m map[string]any, key string) any {
	v, ok := m[key]
	if !ok {
		return nil
	}
	return v
}
func decodeBody(r *http.Request) (map[string]any, error) {
	defer r.Body.Close()
	var out map[string]any
	d := json.NewDecoder(r.Body)
	d.UseNumber()
	if err := d.Decode(&out); err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errors.New("body must be an object")
	}
	return out, nil
}

type apiError struct {
	status  int
	message string
}

func (e apiError) Error() string { return e.message }
func writeError(w http.ResponseWriter, err error) {
	var ae apiError
	if errors.As(err, &ae) {
		http.Error(w, ae.message, ae.status)
		return
	}
	http.Error(w, "database write failed", 500)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	data, e := json.Marshal(v)
	if e != nil {
		http.Error(w, "internal server error", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func writeJSONKeys(w http.ResponseWriter, status int, values map[string]any, keys []string) {
	data := make([]byte, 0, 256)
	data = append(data, '{')
	first := true
	for _, key := range keys {
		value, ok := values[key]
		if !ok {
			continue
		}
		if !first {
			data = append(data, ',')
		}
		first = false
		name, _ := json.Marshal(key)
		encoded, err := json.Marshal(value)
		if err != nil {
			http.Error(w, "internal server error", 500)
			return
		}
		data = append(data, name...)
		data = append(data, ':')
		data = append(data, encoded...)
	}
	data = append(data, '}')
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(status)
	_, _ = w.Write(data)
}
func sendStatus(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	body := http.StatusText(status)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("ETag", userExpressETag([]byte(body)))
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

// sendEntityNotFound mirrors the upstream collection/playlist middleware 404:
// Express res.status(404).send('…') sends the message with Content-Type
// text/html; charset=utf-8 (CollectionController/PlaylistController middleware).
func sendEntityNotFound(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(message)))
	w.Header().Set("ETag", userExpressETag([]byte(message)))
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(message))
}

// respondPlaylistError keeps owner-mismatch 403s as express sendStatus
// equivalents while playlist-missing/inaccessible 404s use the upstream
// 'Playlist not found' HTML body (PlaylistController.js:575-593).
func respondPlaylistError(w http.ResponseWriter, code int) {
	if code == http.StatusNotFound {
		sendEntityNotFound(w, "Playlist not found")
		return
	}
	sendStatus(w, code)
}

// sendText mirrors Express res.send(string): an HTML-typed plain-text body
// without a trailing newline, used for upstream res.send validation messages.
func sendText(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("ETag", userExpressETag([]byte(body)))
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func userExpressETag(body []byte) string {
	sum := sha1.Sum(body)
	return fmt.Sprintf("W/\"%x-%s\"", len(body), strings.TrimRight(base64.StdEncoding.EncodeToString(sum[:]), "="))
}
func bookmarkIndex(bookmarks []any, item string, t float64) int {
	for i, v := range bookmarks {
		if bm, ok := v.(map[string]any); ok && bm["libraryItemId"] == item {
			if n, ok := jsonNumber(bm["time"]); ok && n == t {
				return i
			}
		}
	}
	return -1
}
func newUUID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	raw := hex.EncodeToString(id[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", raw[:8], raw[8:12], raw[12:16], raw[16:20], raw[20:])
}
