// Package emailapi implements SMTP settings and ereader delivery APIs.
package emailapi

import (
	"audiobookshelf-compat/internal/access"
	"audiobookshelf-compat/internal/emailsettings"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/jsonvalue"
	"bytes"
	"context"
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
	"reflect"
	"strings"
	"time"

	"audiobookshelf-compat/internal/store"
)

type Handler struct {
	db   *sql.DB
	emit events.Emitter
}

func New(db *sql.DB, emit events.Emitter) *Handler {
	return &Handler{db: db, emit: emit}
}

// Routes registers the email routes (server/routers/ApiRouter.js /emails). A
// non-admin is answered 404 on the admin routes.
func (h *Handler) Routes(mux *http.ServeMux) {
	adminOnly := func(f httputil.Handler) httputil.Handler { return access.RequireAdmin(http.StatusNotFound, f) }
	mux.Handle("GET /api/emails/settings", adminOnly(h.getSettings))
	mux.Handle("PATCH /api/emails/settings", adminOnly(h.updateSettings))
	mux.Handle("POST /api/emails/test", adminOnly(h.sendTest))
	mux.Handle("POST /api/emails/ereader-devices", adminOnly(h.ereaderDevices))
	mux.Handle("POST /api/emails/send-ebook-to-device", httputil.Handler(h.sendEbook))
}

func (h *Handler) getSettings(w http.ResponseWriter, r *http.Request) error {
	settings, err := emailsettings.Load(r.Context(), h.db)
	if err != nil {
		return httputil.Internal("database error", err)
	}
	return httputil.JSON(w, r, map[string]any{"settings": settings})
}
func (h *Handler) updateSettings(w http.ResponseWriter, r *http.Request) error {
	settings, err := emailsettings.Load(r.Context(), h.db)
	if err != nil {
		return httputil.Internal("database error", err)
	}
	before := settings
	patch := settingsPatch{Settings: settings}
	if json.NewDecoder(r.Body).Decode(&patch) != nil {
		return httputil.Status(400, "")
	}
	settings = patch.Settings
	settings.ID = before.ID
	if len(patch.EreaderDevices) > 0 {
		var value any
		_ = json.Unmarshal(patch.EreaderDevices, &value) // the request decoder already validated the JSON
		if devices, ok := value.([]any); ok {
			settings.EreaderDevices = emailsettings.CleanDevices(devices)
		}
	}
	if !reflect.DeepEqual(before, settings) {
		if err = emailsettings.Save(r.Context(), h.db, settings); err != nil {
			return httputil.Internal("database error", err)
		}
	}
	return httputil.JSON(w, r, map[string]any{"settings": settings})
}

// settingsPatch ignores non-array ereaderDevices like EmailSettings.update.
type settingsPatch struct {
	emailsettings.Settings
	EreaderDevices json.RawMessage `json:"ereaderDevices"`
}

func (h *Handler) ereaderDevices(w http.ResponseWriter, r *http.Request) error {
	var p struct {
		Devices []map[string]any `json:"ereaderDevices"`
	}
	if json.NewDecoder(r.Body).Decode(&p) != nil || p.Devices == nil {
		return httputil.Status(400, "Invalid payload. ereaderDevices array required")
	}
	for _, d := range p.Devices {
		if !jsonvalue.Truthy(d["name"]) || !jsonvalue.Truthy(d["email"]) {
			return httputil.Status(400, "Invalid payload. ereaderDevices array items must have name and email")
		}
	}
	settings, e := emailsettings.Load(r.Context(), h.db)
	if e != nil {
		return httputil.Internal("database error", e)
	}
	provided := make([]any, len(p.Devices))
	for i, device := range p.Devices {
		provided[i] = device
	}
	clean := emailsettings.CleanDevices(provided)
	changed := !reflect.DeepEqual(settings.EreaderDevices, clean)
	settings.EreaderDevices = clean
	if changed {
		if e = emailsettings.Save(r.Context(), h.db, settings); e != nil {
			return httputil.Internal("database error", e)
		}
		h.emit.ToAdmins("ereader-devices-updated", map[string]any{"ereaderDevices": clean})
	}
	return httputil.JSON(w, r, map[string]any{"ereaderDevices": clean})
}
func (h *Handler) sendTest(w http.ResponseWriter, r *http.Request) error {
	settings, e := emailsettings.Load(r.Context(), h.db)
	if e != nil {
		return httputil.Internal("database error", e)
	}
	if e = verifySMTP(r.Context(), settings); e != nil {
		return httputil.Status(http.StatusBadRequest, "Failed to verify SMTP connection configuration")
	}
	to := deref(settings.TestAddress)
	if to == "" {
		to = deref(settings.FromAddress)
	}
	if e = sendMail(r.Context(), settings, to, "Test email from Audiobookshelf", "Success!", nil); e != nil {
		message := e.Error()
		if message == "" {
			message = "Failed to send test email"
		}
		return httputil.Status(http.StatusBadRequest, message)
	}
	return httputil.OK(w, r)
}
func (h *Handler) sendEbook(w http.ResponseWriter, r *http.Request) error {
	u, _ := store.UserFromContext(r.Context())
	var p struct {
		LibraryItemID string `json:"libraryItemId"`
		DeviceName    string `json:"deviceName"`
	}
	if json.NewDecoder(r.Body).Decode(&p) != nil || p.LibraryItemID == "" || p.DeviceName == "" {
		return httputil.Status(400, "")
	}
	settings, e := emailsettings.Load(r.Context(), h.db)
	if e != nil {
		return httputil.Internal("database error", e)
	}
	var device emailsettings.Device
	for _, candidate := range settings.EreaderDevices {
		if jsonvalue.String(candidate["name"]) == p.DeviceName {
			device = candidate
			break
		}
	}
	if device == nil {
		return httputil.Status(404, "Ereader device not found")
	}
	if !emailsettings.DeviceAllowed(device, u) {
		return httputil.Status(403, "")
	}
	var folder, mediaID, mediaType string
	e = h.db.QueryRowContext(r.Context(), `SELECT libraryId,mediaId,mediaType FROM libraryItems WHERE id=?`, p.LibraryItemID).Scan(&folder, &mediaID, &mediaType)
	if e != nil {
		return httputil.Status(404, "Library item not found")
	}
	perms := access.For(r.Context(), u)
	if !perms.IsAdmin() && !perms.Library(folder) {
		return httputil.Status(403, "")
	}
	if mediaType != "book" {
		return httputil.Status(404, "Ebook file not found")
	}
	var explicit bool
	var tagsRaw, ebookRaw []byte
	e = h.db.QueryRowContext(r.Context(), `SELECT explicit,tags,ebookFile FROM books WHERE id=?`, mediaID).Scan(&explicit, &tagsRaw, &ebookRaw)
	var tags []string
	_ = json.Unmarshal(tagsRaw, &tags)
	if e != nil || !perms.Content(access.Access{Explicit: explicit, Tags: tags}) {
		return httputil.Status(403, "")
	}
	var ebook struct {
		Metadata struct {
			Filename string `json:"filename"`
			Path     string `json:"path"`
		} `json:"metadata"`
	}
	if json.Unmarshal(ebookRaw, &ebook) != nil || ebook.Metadata.Path == "" {
		return httputil.Status(404, "Ebook file not found")
	}
	f, e := os.Open(ebook.Metadata.Path)
	if e != nil {
		return httputil.Status(404, "Ebook file not found")
	}
	defer f.Close()
	data, e := io.ReadAll(io.LimitReader(f, 512<<20))
	if e != nil {
		return httputil.Internal("Failed to read ebook file", e)
	}
	if e = verifySMTP(r.Context(), settings); e != nil {
		return httputil.Status(http.StatusBadRequest, "Failed to verify SMTP connection configuration")
	}
	if e = sendMail(r.Context(), settings, jsonvalue.String(device["email"]), "Here is your Ebook!", "", &attachment{name: ebook.Metadata.Filename, data: data}); e != nil {
		message := e.Error()
		if message == "" {
			message = "Failed to send ebook to device"
		}
		return httputil.Status(http.StatusBadRequest, message)
	}
	return httputil.OK(w, r)
}

type attachment struct {
	name string
	data []byte
}

func verifySMTP(ctx context.Context, settings emailsettings.Settings) error {
	return smtpConnect(ctx, settings, func(c *smtp.Client) error { return c.Noop() })
}
func sendMail(ctx context.Context, settings emailsettings.Settings, to, subject, body string, a *attachment) error {
	return smtpConnect(ctx, settings, func(c *smtp.Client) error {
		from := deref(settings.FromAddress)
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
func smtpConnect(ctx context.Context, settings emailsettings.Settings, fn func(*smtp.Client) error) error {
	host := deref(settings.Host)
	if host == "" {
		return fmt.Errorf("SMTP host is required")
	}
	port := int(settings.Port)
	if port <= 0 {
		port = 465
	}
	secure := bool(settings.Secure)
	if port != 465 {
		secure = false
	}
	reject := bool(settings.RejectUnauthorized)
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
	user := deref(settings.User)
	pass := deref(settings.Pass)
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
func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
