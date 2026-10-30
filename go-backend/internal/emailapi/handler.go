// Package emailapi implements SMTP settings and ereader delivery APIs.
package emailapi

import (
	"audiobookshelf-compat/internal/dbtime"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/store"
)

type Handler struct {
	db   *sql.DB
	cfg  config.Config
	emit func(string, string, any)
}

func New(db *sql.DB, cfg config.Config, emit func(string, string, any)) *Handler {
	return &Handler{db: db, cfg: cfg, emit: emit}
}
func (h *Handler) Handle(w http.ResponseWriter, r *http.Request, u store.User) bool {
	switch {
	case r.URL.Path == "/api/emails/settings" && r.Method == http.MethodGet:
		if !admin(u) {
			writeStatus(w, http.StatusNotFound)
		} else {
			h.settings(w, r, false)
		}
	case r.URL.Path == "/api/emails/settings" && r.Method == http.MethodPatch:
		if !admin(u) {
			writeStatus(w, http.StatusNotFound)
		} else {
			h.settings(w, r, true)
		}
	case r.URL.Path == "/api/emails/test" && r.Method == http.MethodPost:
		if !admin(u) {
			writeStatus(w, http.StatusNotFound)
		} else {
			h.sendTest(w, r)
		}
	case r.URL.Path == "/api/emails/ereader-devices" && r.Method == http.MethodPost:
		if !admin(u) {
			writeStatus(w, http.StatusNotFound)
		} else {
			h.ereaderDevices(w, r)
		}
	case r.URL.Path == "/api/emails/send-ebook-to-device" && r.Method == http.MethodPost:
		h.sendEbook(w, r, u)
	default:
		return false
	}
	return true
}
func admin(u store.User) bool { return u.Type == "admin" || u.Type == "root" }
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
func (h *Handler) load(ctx context.Context) (map[string]any, error) {
	var raw []byte
	e := h.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='email-settings'`).Scan(&raw)
	if e != nil {
		return nil, e
	}
	var m map[string]any
	e = json.Unmarshal(raw, &m)
	return m, e
}
func (h *Handler) save(ctx context.Context, m map[string]any) error {
	b, e := json.Marshal(m)
	if e != nil {
		return e
	}
	_, e = h.db.ExecContext(ctx, `UPDATE settings SET value=?,updatedAt=? WHERE key='email-settings'`, string(b), dbtime.Now())
	return e
}
func (h *Handler) settings(w http.ResponseWriter, r *http.Request, patch bool) {
	m, e := h.load(r.Context())
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	if patch {
		var p map[string]any
		if json.NewDecoder(r.Body).Decode(&p) != nil {
			w.WriteHeader(400)
			return
		}
		for _, k := range []string{"host", "port", "secure", "rejectUnauthorized", "user", "pass", "testAddress", "fromAddress", "ereaderDevices"} {
			v, ok := p[k]
			if !ok {
				continue
			}
			if k == "port" {
				if n, ok := numeric(v); ok && n >= 0 && n <= 65535 {
					v = n
				} else {
					v = 465
				}
			}
			if k == "secure" || k == "rejectUnauthorized" {
				v = v == true
			}
			if k == "ereaderDevices" {
				if list, ok := v.([]any); ok {
					v = cleanDevices(list)
				} else {
					continue
				}
			}
			m[k] = v
		}
		if e = h.save(r.Context(), m); e != nil {
			http.Error(w, "database error", 500)
			return
		}
	}
	write(w, map[string]any{"settings": m})
}
func (h *Handler) ereaderDevices(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Devices []map[string]any `json:"ereaderDevices"`
	}
	if json.NewDecoder(r.Body).Decode(&p) != nil || p.Devices == nil {
		http.Error(w, "Invalid payload. ereaderDevices array required", 400)
		return
	}
	for _, d := range p.Devices {
		if d["name"] == nil || d["name"] == "" || d["email"] == nil || d["email"] == "" {
			http.Error(w, "Invalid payload. ereaderDevices array items must have name and email", 400)
			return
		}
	}
	m, e := h.load(r.Context())
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	clean := make([]any, len(p.Devices))
	for i, d := range p.Devices {
		if d["availabilityOption"] == nil {
			d["availabilityOption"] = "adminOrUp"
		}
		clean[i] = d
	}
	m["ereaderDevices"] = clean
	if e = h.save(r.Context(), m); e != nil {
		http.Error(w, "database error", 500)
		return
	}
	if h.emit != nil {
		h.emit("@admins", "ereader-devices-updated", map[string]any{"ereaderDevices": clean})
	}
	write(w, map[string]any{"ereaderDevices": clean})
}
func (h *Handler) sendTest(w http.ResponseWriter, r *http.Request) {
	m, e := h.load(r.Context())
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	if e = verifySMTP(r.Context(), m); e != nil {
		http.Error(w, "Failed to verify SMTP connection configuration", 400)
		return
	}
	to, _ := m["testAddress"].(string)
	if to == "" {
		to, _ = m["fromAddress"].(string)
	}
	if e = sendMail(r.Context(), m, to, "Test email from Audiobookshelf", "Success!", nil); e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	w.WriteHeader(200)
}
func (h *Handler) sendEbook(w http.ResponseWriter, r *http.Request, u store.User) {
	var p struct {
		LibraryItemID string `json:"libraryItemId"`
		DeviceName    string `json:"deviceName"`
	}
	if json.NewDecoder(r.Body).Decode(&p) != nil || p.LibraryItemID == "" || p.DeviceName == "" {
		w.WriteHeader(400)
		return
	}
	m, e := h.load(r.Context())
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	device := device(m, p.DeviceName)
	if device == nil {
		http.Error(w, "Ereader device not found", 404)
		return
	}
	if !deviceAllowed(device, u) {
		w.WriteHeader(403)
		return
	}
	var folder, mediaID, mediaType string
	e = h.db.QueryRowContext(r.Context(), `SELECT libraryId,mediaId,mediaType FROM libraryItems WHERE id=?`, p.LibraryItemID).Scan(&folder, &mediaID, &mediaType)
	if e != nil {
		http.Error(w, "Library item not found", 404)
		return
	}
	if !libraryAllowed(u, folder) {
		w.WriteHeader(403)
		return
	}
	if mediaType != "book" {
		http.Error(w, "Ebook file not found", 404)
		return
	}
	var explicit int
	var tagsRaw, ebookRaw []byte
	e = h.db.QueryRowContext(r.Context(), `SELECT explicit,tags,ebookFile FROM books WHERE id=?`, mediaID).Scan(&explicit, &tagsRaw, &ebookRaw)
	if e != nil || !canAccessContent(u, explicit, tagsRaw) {
		w.WriteHeader(403)
		return
	}
	var ebook struct {
		Metadata struct {
			Filename string `json:"filename"`
			Path     string `json:"path"`
		} `json:"metadata"`
	}
	if json.Unmarshal(ebookRaw, &ebook) != nil || ebook.Metadata.Path == "" {
		http.Error(w, "Ebook file not found", 404)
		return
	}
	f, e := os.Open(ebook.Metadata.Path)
	if e != nil {
		http.Error(w, "Ebook file not found", 404)
		return
	}
	defer f.Close()
	data, e := io.ReadAll(io.LimitReader(f, 512<<20))
	if e != nil {
		http.Error(w, "Failed to read ebook file", 500)
		return
	}
	if e = verifySMTP(r.Context(), m); e != nil {
		http.Error(w, "Failed to verify SMTP connection configuration", 400)
		return
	}
	from, _ := m["fromAddress"].(string)
	if e = sendMail(r.Context(), m, device["email"].(string), "Here is your Ebook!", "", &attachment{name: ebook.Metadata.Filename, data: data}); e != nil {
		http.Error(w, e.Error(), 400)
		return
	}
	_ = from
	w.WriteHeader(200)
}

type attachment struct {
	name string
	data []byte
}

func verifySMTP(ctx context.Context, m map[string]any) error {
	return smtpConnect(ctx, m, func(c *smtp.Client) error { return c.Noop() })
}
func sendMail(ctx context.Context, m map[string]any, to, subject, body string, a *attachment) error {
	return smtpConnect(ctx, m, func(c *smtp.Client) error {
		from, _ := m["fromAddress"].(string)
		if from == "" || to == "" {
			return fmt.Errorf("SMTP from and recipient addresses are required")
		}
		if e := c.Mail(from); e != nil {
			return e
		}
		if e := c.Rcpt(to); e != nil {
			return e
		}
		w, e := c.Data()
		if e != nil {
			return e
		}
		msg := buildMessage(from, to, subject, body, a)
		_, e = w.Write(msg)
		if e != nil {
			_ = w.Close()
			return e
		}
		return w.Close()
	})
}
func smtpConnect(ctx context.Context, m map[string]any, fn func(*smtp.Client) error) error {
	host, _ := m["host"].(string)
	if host == "" {
		return fmt.Errorf("SMTP host is required")
	}
	port := 465
	if n, ok := numeric(m["port"]); ok && n > 0 {
		port = int(n)
	}
	secure := m["secure"] == true
	if port != 465 {
		secure = false
	}
	reject := m["rejectUnauthorized"] != false
	timeout := 8 * time.Second
	dialer := net.Dialer{Timeout: timeout}
	var conn net.Conn
	var e error
	addr := net.JoinHostPort(host, fmt.Sprint(port))
	if secure {
		conn, e = tls.DialWithDialer(&dialer, "tcp", addr, &tls.Config{ServerName: host, InsecureSkipVerify: !reject})
	} else {
		conn, e = dialer.DialContext(ctx, "tcp", addr)
	}
	if e != nil {
		return e
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	client, e := smtp.NewClient(conn, host)
	if e != nil {
		return e
	}
	defer client.Close()
	if !secure {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if e = client.StartTLS(&tls.Config{ServerName: host, InsecureSkipVerify: !reject}); e != nil {
				return e
			}
		}
	}
	user, _ := m["user"].(string)
	pass, _ := m["pass"].(string)
	if user != "" {
		if e = client.Auth(smtp.PlainAuth("", user, pass, host)); e != nil {
			return e
		}
	}
	return fn(client)
}
func buildMessage(from, to, subject, body string, a *attachment) []byte {
	var b bytes.Buffer
	header := textproto.MIMEHeader{}
	header.Set("From", from)
	header.Set("To", to)
	header.Set("Subject", subject)
	if a == nil {
		header.Set("MIME-Version", "1.0")
		header.Set("Content-Type", "text/plain; charset=utf-8")
		for k, v := range header {
			fmt.Fprintf(&b, "%s: %s\r\n", k, strings.Join(v, ", "))
		}
		b.WriteString("\r\n" + body)
		return b.Bytes()
	}
	boundary := "abs-ebook-boundary"
	header.Set("MIME-Version", "1.0")
	header.Set("Content-Type", "multipart/mixed; boundary="+boundary)
	for k, v := range header {
		fmt.Fprintf(&b, "%s: %s\r\n", k, strings.Join(v, ", "))
	}
	b.WriteString("\r\n--" + boundary + "\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<div dir=\"auto\"></div>\r\n--" + boundary + "\r\nContent-Type: application/octet-stream; name*=utf-8''" + urlQuery(a.name) + "\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename*=utf-8''" + urlQuery(a.name) + "\r\n\r\n")
	enc := base64.StdEncoding.EncodeToString(a.data)
	for i := 0; i < len(enc); i += 76 {
		end := i + 76
		if end > len(enc) {
			end = len(enc)
		}
		b.WriteString(enc[i:end] + "\r\n")
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.Bytes()
}
func urlQuery(s string) string {
	return strings.ReplaceAll(url.PathEscape(filepath.Base(s)), "+", "%20")
}
func numeric(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), true
	case int:
		return x, true
	case string:
		var n int
		_, e := fmt.Sscan(x, &n)
		return n, e == nil
	}
	return 0, false
}
func cleanDevices(a []any) []any {
	out := []any{}
	valid := map[string]bool{"adminOrUp": true, "userOrUp": true, "guestOrUp": true, "specificUsers": true}
	for _, raw := range a {
		d, ok := raw.(map[string]any)
		if !ok || d["name"] == nil || d["email"] == nil {
			continue
		}
		level, _ := d["availabilityOption"].(string)
		if !valid[level] || (level == "specificUsers" && len(anyArray(d["users"])) == 0) {
			d["availabilityOption"] = "adminOrUp"
		}
		if d["availabilityOption"] != "specificUsers" {
			d["users"] = []any{}
		}
		out = append(out, d)
	}
	return out
}
func anyArray(v any) []any { a, _ := v.([]any); return a }
func device(m map[string]any, name string) map[string]any {
	for _, x := range anyArray(m["ereaderDevices"]) {
		d, _ := x.(map[string]any)
		if d["name"] == name {
			return d
		}
	}
	return nil
}
func deviceAllowed(d map[string]any, u store.User) bool {
	level, _ := d["availabilityOption"].(string)
	if level == "" {
		level = "adminOrUp"
	}
	switch level {
	case "adminOrUp":
		return admin(u)
	case "userOrUp":
		return admin(u) || u.Type == "user"
	case "guestOrUp":
		return true
	case "specificUsers":
		for _, id := range anyArray(d["users"]) {
			if id == u.ID {
				return true
			}
		}
	}
	return false
}
func libraryAllowed(u store.User, id string) bool {
	if admin(u) {
		return true
	}
	var p map[string]any
	_ = json.Unmarshal(u.Permissions, &p)
	if p["accessAllLibraries"] == true {
		return true
	}
	for _, v := range anyArray(p["librariesAccessible"]) {
		if v == id {
			return true
		}
	}
	return false
}
func canAccessContent(u store.User, explicit int, tagsRaw []byte) bool {
	var p map[string]any
	_ = json.Unmarshal(u.Permissions, &p)
	if explicit != 0 && p["accessExplicitContent"] != true {
		return false
	}
	var tags []string
	_ = json.Unmarshal(tagsRaw, &tags)
	if p["accessAllTags"] == true {
		return true
	}
	var selected []string
	_ = json.Unmarshal(mustJSON(p["itemTagsSelected"]), &selected)
	deny := p["selectedTagsNotAccessible"] == true
	if len(tags) == 0 {
		return deny
	}
	matched := false
	for _, tag := range tags {
		has := false
		for _, s := range selected {
			if tag == s {
				has = true
			}
		}
		if deny && has {
			return false
		}
		if !deny && has {
			matched = true
		}
	}
	return deny || matched
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
