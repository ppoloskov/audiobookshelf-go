// Package notifications implements the notification settings and Apprise APIs.
package notifications

import (
	"audiobookshelf-compat/internal/dbtime"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
)

type Handler struct {
	db   *sql.DB
	emit func(string, string, any)
}

func New(db *sql.DB, _ config.Config, emit func(string, string, any)) *Handler {
	return &Handler{db: db, emit: emit}
}
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request, user store.User) bool {
	p := r.URL.Path
	if (strings.HasPrefix(p, "/api/notifications") || p == "/api/notificationdata") && !admin(user.Type) {
		writeStatus(w, http.StatusForbidden)
		return true
	}
	switch {
	case p == "/api/notifications" && r.Method == http.MethodGet:
		h.get(w, r, false)
	case p == "/api/notifications" && r.Method == http.MethodPatch:
		h.update(w, r)
	case p == "/api/notificationdata" && r.Method == http.MethodGet:
		h.get(w, r, true)
	case p == "/api/notifications/test" && r.Method == http.MethodGet:
		h.testEvent(w, r)
	case p == "/api/notifications" && r.Method == http.MethodPost:
		h.create(w, r)
	case strings.HasPrefix(p, "/api/notifications/") && strings.HasSuffix(p, "/test") && r.Method == http.MethodGet:
		h.testNotification(w, r)
	case strings.HasPrefix(p, "/api/notifications/") && r.Method == http.MethodDelete:
		h.delete(w, r)
	case strings.HasPrefix(p, "/api/notifications/") && r.Method == http.MethodPatch:
		h.updateNotification(w, r)
	default:
		return false
	}
	return true
}
func (h *Handler) load(ctx context.Context) (map[string]any, error) {
	var raw []byte
	if err := h.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='notification-settings'`).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return map[string]any{
				"id": "notification-settings", "appriseType": "api", "appriseApiUrl": nil,
				"notifications": []any{}, "maxFailedAttempts": 5,
				"maxNotificationQueue": 20, "notificationDelay": 1000,
			}, nil
		}
		return nil, err
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}
func (h *Handler) save(ctx context.Context, v map[string]any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	now := dbtime.Now()
	_, e = h.db.ExecContext(ctx, `INSERT INTO settings (key,value,createdAt,updatedAt) VALUES ('notification-settings',?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updatedAt=excluded.updatedAt`, string(b), now, now)
	return e
}
func admin(t string) bool { return t == "admin" || t == "root" }
func writeStatus(w http.ResponseWriter, status int) {
	body := http.StatusText(status)
	sum := sha1.Sum([]byte(body))
	hash := strings.TrimRight(base64.StdEncoding.EncodeToString(sum[:]), "=")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("ETag", fmt.Sprintf("W/\"%x-%s\"", len(body), hash))
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
func writeExpressSend(w http.ResponseWriter, status int, body string) {
	sum := sha1.Sum([]byte(body))
	hash := strings.TrimRight(base64.StdEncoding.EncodeToString(sum[:]), "=")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("ETag", fmt.Sprintf("W/\"%x-%s\"", len(body), hash))
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}
func (h *Handler) get(w http.ResponseWriter, r *http.Request, data bool) {
	s, e := h.load(r.Context())
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	if data {
		write(w, map[string]any{"events": events})
		return
	}
	write(w, map[string]any{"data": map[string]any{"events": events}, "settings": s})
}
func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	var payload any
	if json.NewDecoder(r.Body).Decode(&payload) != nil {
		w.WriteHeader(400)
		return
	}
	p, ok := payload.(map[string]any)
	if !ok {
		writeStatus(w, http.StatusOK)
		return
	}
	s, e := h.load(r.Context())
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	updated := false
	if v, ok := p["appriseApiUrl"]; !ok {
		s["appriseApiUrl"] = nil
		updated = true // upstream compares undefined against the current null value
	} else if v == nil || v == "" || v == false || v == float64(0) {
		if !strictEqual(s["appriseApiUrl"], nil) {
			updated = true
		}
		s["appriseApiUrl"] = nil
	} else {
		if !strictEqual(s["appriseApiUrl"], v) {
			updated = true
		}
		s["appriseApiUrl"] = v
	}
	for k, fallback := range map[string]float64{"maxFailedAttempts": 5, "maxNotificationQueue": 20} {
		if v, ok := p[k]; !ok {
			if current, valid := number(s[k]); !valid || current != fallback {
				updated = true
			}
			s[k] = fallback
		} else if n, ok := number(v); ok {
			if current, valid := number(s[k]); !valid || current != n {
				updated = true
			}
			s[k] = n
		} else {
			if current, valid := number(s[k]); !valid || current != fallback {
				updated = true
			}
			s[k] = fallback
		}
	}
	if updated {
		if e = h.save(r.Context(), s); e != nil {
			http.Error(w, "database error", 500)
			return
		}
	}
	writeStatus(w, http.StatusOK)
}
func strictEqual(a, b any) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case string:
		y, ok := b.(string)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	case int:
		switch y := b.(type) {
		case int:
			return x == y
		case float64:
			return float64(x) == y
		}
	}
	return false
}
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case int:
		return x != 0
	default:
		return true
	}
}
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	p := map[string]any{}
	_ = json.NewDecoder(r.Body).Decode(&p)
	event, _ := p["eventName"].(string)
	urls, _ := p["urls"].([]any)
	if event == "" || len(urls) == 0 {
		write(w, h.mustLoad(r))
		return
	}
	id, e := id()
	if e != nil {
		http.Error(w, "internal error", 500)
		return
	}
	s, e := h.load(r.Context())
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	items := array(s["notifications"])
	libraryID := p["libraryId"]
	if !truthy(libraryID) {
		libraryID = nil
	}
	notificationType := p["type"]
	if !truthy(notificationType) {
		notificationType = nil
	}
	n := map[string]any{"id": id, "libraryId": libraryID, "eventName": event, "urls": urls, "enabled": truthy(p["enabled"]), "type": notificationType, "lastFiredAt": nil, "lastAttemptFailed": false, "numConsecutiveFailedAttempts": 0, "numTimesFired": 0, "createdAt": time.Now().UnixMilli()}
	if value, ok := p["titleTemplate"]; ok {
		n["titleTemplate"] = value
	}
	if value, ok := p["bodyTemplate"]; ok {
		n["bodyTemplate"] = value
	}
	items = append(items, n)
	s["notifications"] = items
	if h.save(r.Context(), s) != nil {
		http.Error(w, "database error", 500)
		return
	}
	write(w, s)
}
func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	s, e := h.load(r.Context())
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	id := lastID(r.URL.Path)
	a := array(s["notifications"])
	out := []any{}
	removed := false
	for _, raw := range a {
		n, _ := raw.(map[string]any)
		if n["id"] == id {
			removed = true
			continue
		}
		out = append(out, raw)
	}
	if !removed {
		writeStatus(w, http.StatusNotFound)
		return
	}
	s["notifications"] = out
	if h.save(r.Context(), s) != nil {
		http.Error(w, "database error", 500)
		return
	}
	write(w, s)
}
func (h *Handler) updateNotification(w http.ResponseWriter, r *http.Request) {
	s, e := h.load(r.Context())
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	if h.find(s, lastID(r.URL.Path)) == nil {
		writeStatus(w, http.StatusNotFound)
		return
	}
	var payload any
	if json.NewDecoder(r.Body).Decode(&payload) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	p, ok := payload.(map[string]any)
	if !ok {
		write(w, s)
		return
	}
	id, _ := p["id"].(string)
	n := h.find(s, id)
	if n != nil {
		updated := false
		if truthy(p["enabled"]) && n["enabled"] != true {
			n["lastFiredAt"] = nil
			n["lastAttemptFailed"] = false
			n["numConsecutiveFailedAttempts"] = 0
			updated = true
		}
		for _, k := range []string{"libraryId", "eventName", "urls", "titleTemplate", "bodyTemplate", "enabled", "type"} {
			v, present := p[k]
			if !present {
				continue
			}
			if k == "urls" {
				if !reflect.DeepEqual(v, n[k]) {
					n[k] = v
					updated = true
				}
			} else if !strictEqual(n[k], v) {
				n[k] = v
				updated = true
			}
		}
		if updated {
			if h.save(r.Context(), s) != nil {
				http.Error(w, "database error", 500)
				return
			}
		}
	}
	write(w, s)
}
func (h *Handler) testEvent(w http.ResponseWriter, r *http.Request) {
	s, e := h.load(r.Context())
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	data := events[len(events)-1].(map[string]any)
	_, _ = h.deliverActive(r.Context(), s, "onTest", data["testData"].(map[string]any), r.URL.Query().Get("fail") == "1")
	writeStatus(w, http.StatusOK)
}

// TriggerEvent is the runtime hook for source lifecycle notifications (backup
// completion/failure and podcast download/feed failures). Callers pass the
// event's source-shaped template data; unknown event names are ignored just as
// an event with no active notifications is.
func (h *Handler) TriggerEvent(ctx context.Context, eventName string, data map[string]any) error {
	if eventData(eventName) == nil {
		return nil
	}
	s, err := h.load(ctx)
	if err != nil {
		return err
	}
	if _, err = h.deliverActive(ctx, s, eventName, data, false); err != nil {
		return err
	}
	return nil
}

func (h *Handler) testNotification(w http.ResponseWriter, r *http.Request) {
	s, e := h.load(r.Context())
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	n := h.find(s, lastID(strings.TrimSuffix(r.URL.Path, "/test")))
	if n == nil {
		writeStatus(w, http.StatusNotFound)
		return
	}
	endpoint, _ := s["appriseApiUrl"].(string)
	if endpoint == "" {
		writeExpressSend(w, http.StatusBadRequest, "Apprise is not configured")
		return
	}
	event := eventData(n["eventName"].(string))
	if event == nil {
		writeStatus(w, http.StatusInternalServerError)
		return
	}
	if h.deliver(r.Context(), endpoint, payload(n, event["testData"].(map[string]any))) == nil {
		writeStatus(w, http.StatusOK)
	} else {
		writeStatus(w, http.StatusInternalServerError)
	}
}
func (h *Handler) deliverActive(ctx context.Context, s map[string]any, event string, data map[string]any, fail bool) (bool, error) {
	endpoint, _ := s["appriseApiUrl"].(string)
	if endpoint == "" {
		return false, nil
	}
	ok := true
	for _, raw := range array(s["notifications"]) {
		n, _ := raw.(map[string]any)
		if n["enabled"] != true || n["eventName"] != event {
			continue
		}
		success := !fail && h.deliver(ctx, endpoint, payload(n, data)) == nil
		n["lastFiredAt"] = time.Now().UnixMilli()
		n["lastAttemptFailed"] = !success
		count, _ := number(n["numTimesFired"])
		n["numTimesFired"] = count + 1
		if success {
			n["numConsecutiveFailedAttempts"] = 0
		} else {
			current, _ := number(n["numConsecutiveFailedAttempts"])
			n["numConsecutiveFailedAttempts"] = current + 1
			max, _ := number(s["maxFailedAttempts"])
			if current+1 >= max {
				n["enabled"] = false
			}
			ok = false
		}
	}
	if err := h.save(ctx, s); err != nil {
		return false, err
	}
	if h.emit != nil {
		h.emit("*", "notifications_updated", s)
	}
	return ok, nil
}
func (h *Handler) deliver(ctx context.Context, endpoint string, p map[string]any) error {
	u, e := url.Parse(endpoint)
	if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("invalid Apprise URL")
	}
	b, _ := json.Marshal(p)
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(string(b)))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 6 * time.Second}
	res, e := client.Do(req)
	if e != nil {
		return e
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("Apprise returned %d", res.StatusCode)
	}
	return nil
}
func (h *Handler) find(s map[string]any, id string) map[string]any {
	for _, raw := range array(s["notifications"]) {
		n, _ := raw.(map[string]any)
		if n["id"] == id {
			return n
		}
	}
	return nil
}
func (h *Handler) mustLoad(r *http.Request) map[string]any { s, _ := h.load(r.Context()); return s }
func payload(n, data map[string]any) map[string]any {
	p := map[string]any{"urls": n["urls"]}
	if value, ok := n["titleTemplate"]; ok {
		p["title"] = template(value, data)
	}
	if value, ok := n["bodyTemplate"]; ok {
		p["body"] = template(value, data)
	}
	return p
}
func template(raw any, data map[string]any) any {
	s, ok := raw.(string)
	if !ok {
		return raw
	}
	for k, v := range data {
		if v == nil || v == "" || v == false || v == float64(0) || v == int(0) {
			continue
		}
		s = strings.ReplaceAll(s, "{{"+k+"}}", fmt.Sprint(v))
		s = strings.ReplaceAll(s, "{{ "+k+" }}", fmt.Sprint(v))
	}
	return s
}
func eventData(name string) map[string]any {
	for _, raw := range events {
		m := raw.(map[string]any)
		if m["name"] == name {
			return m
		}
	}
	return nil
}
func array(v any) []any {
	a, _ := v.([]any)
	if a == nil {
		return []any{}
	}
	return a
}
func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case string:
		var f float64
		_, e := fmt.Sscan(n, &f)
		return f, e == nil
	}
	return 0, false
}
func id() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	x := hex.EncodeToString(b)
	return x[:8] + "-" + x[8:12] + "-" + x[12:16] + "-" + x[16:20] + "-" + x[20:], nil
}
func lastID(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

var events = []any{
	map[string]any{"name": "onPodcastEpisodeDownloaded", "requiresLibrary": true, "libraryMediaType": "podcast", "description": "Triggered when a podcast episode is auto-downloaded", "descriptionKey": "NotificationOnEpisodeDownloadedDescription", "variables": []string{"libraryItemId", "libraryId", "podcastTitle", "podcastAuthor", "podcastDescription", "podcastGenres", "episodeTitle", "episodeSubtitle", "episodeDescription", "libraryName", "episodeId", "mediaTags"}, "defaults": map[string]string{"title": "New {{podcastTitle}} Episode!", "body": "{{episodeTitle}} has been added to {{libraryName}} library."}, "testData": map[string]any{"libraryItemId": "li_notification_test", "libraryId": "lib_test", "libraryName": "Podcasts", "mediaTags": "TestTag1, TestTag2", "podcastTitle": "Abs Test Podcast", "podcastAuthor": "Audiobookshelf", "podcastDescription": "Description of the Abs Test Podcast belongs here.", "podcastGenres": "TestGenre1, TestGenre2", "episodeId": "ep_notification_test", "episodeTitle": "Successful Test Episode", "episodeSubtitle": "Episode Subtitle", "episodeDescription": "Some description of the podcast episode."}},
	map[string]any{"name": "onBackupCompleted", "requiresLibrary": false, "description": "Triggered when a backup is completed", "descriptionKey": "NotificationOnBackupCompletedDescription", "variables": []string{"completionTime", "backupPath", "backupSize", "backupCount", "removedOldest"}, "defaults": map[string]string{"title": "Backup Completed", "body": "Backup has been completed successfully.\n\nPath: {{backupPath}}\nSize: {{backupSize}} bytes\nCount: {{backupCount}}\nRemoved Oldest: {{removedOldest}}"}, "testData": map[string]any{"completionTime": "12:00 AM", "backupPath": "path/to/backup", "backupSize": "1.23 MB", "backupCount": "1", "removedOldest": "false"}},
	map[string]any{"name": "onBackupFailed", "requiresLibrary": false, "description": "Triggered when a backup fails", "descriptionKey": "NotificationOnBackupFailedDescription", "variables": []string{"errorMsg"}, "defaults": map[string]string{"title": "Backup Failed", "body": "Backup failed, check ABS logs for more information.\nError message: {{errorMsg}}"}, "testData": map[string]any{"errorMsg": "Example error message"}},
	map[string]any{"name": "onRSSFeedFailed", "requiresLibrary": true, "description": "Triggered when the RSS feed request fails for an automatic episode download", "descriptionKey": "NotificationOnRSSFeedFailedDescription", "variables": []string{"feedUrl", "numFailed", "title"}, "defaults": map[string]string{"title": "RSS Feed Request Failed", "body": "Failed to request RSS feed for {{title}}.\nFeed URL: {{feedUrl}}\nNumber of failed attempts: {{numFailed}}"}, "testData": map[string]any{"title": "Test RSS Feed", "feedUrl": "https://example.com/rss", "numFailed": 3}},
	map[string]any{"name": "onRSSFeedDisabled", "requiresLibrary": true, "description": "Triggered when automatic episode downloads are disabled due to too many failed attempts", "descriptionKey": "NotificationOnRSSFeedDisabledDescription", "variables": []string{"feedUrl", "numFailed", "title"}, "defaults": map[string]string{"title": "Podcast Episode Download Schedule Disabled", "body": "Automatic episode downloads for {{title}} have been disabled due to too many failed RSS feed requests.\nFeed URL: {{feedUrl}}\nNumber of failed attempts: {{numFailed}}"}, "testData": map[string]any{"title": "Test RSS Feed", "feedUrl": "https://example.com/rss", "numFailed": 5}},
	map[string]any{"name": "onTest", "requiresLibrary": false, "description": "Event for testing the notification system", "descriptionKey": "NotificationOnTestDescription", "variables": []string{"version"}, "defaults": map[string]string{"title": "Test Notification on Abs {{version}}", "body": "Test notification body for abs {{version}}."}, "testData": map[string]any{"version": "v2.37.2"}},
}
