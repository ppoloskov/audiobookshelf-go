// Package mail owns the email settings routes, the e-reader device list, the
// test email and "send ebook to device". The SMTP transport and MIME building
// are in transport.go and take plain arguments.
package mail

import (
	"cmp"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"slices"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/item"
	"audiobookshelf-compat/internal/settings"
)

const (
	verifyFailed       = "Failed to verify SMTP connection configuration"
	maxAttachmentBytes = 512 << 20 // largest ebook that is mailed
)

// Handler serves the /api/emails routes.
type Handler struct {
	db       *db.DB
	settings *settings.Store
	emit     events.Emitter
}

// New returns a Handler. The email settings live in store; d loads the library
// item of send-ebook-to-device.
func New(d *db.DB, store *settings.Store, emit events.Emitter) *Handler {
	return &Handler{db: d, settings: store, emit: emit}
}

// Routes registers the email routes. The settings, test and device-list routes
// are admin only and answer 404, not 403, to others (EmailController
// adminMiddleware). send-ebook-to-device is open to any authenticated user.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.Handle("GET /api/emails/settings", adminOrNotFound(h.getSettings))
	mux.Handle("PATCH /api/emails/settings", adminOrNotFound(h.updateSettings))
	mux.Handle("POST /api/emails/test", adminOrNotFound(h.sendTest))
	mux.Handle("POST /api/emails/ereader-devices", adminOrNotFound(h.updateDevices))
	mux.Handle("POST /api/emails/send-ebook-to-device", httpx.Handler(h.sendEbook))
}

func adminOrNotFound(next httpx.Handler) httpx.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		if p := auth.User(r.Context()); !p.IsAdmin() || !bool(p.IsActive) {
			return httpx.NotFound()
		}
		return next(w, r)
	}
}

type settingsResponse struct {
	Settings settings.Email `json:"settings"`
}

func (h *Handler) getSettings(w http.ResponseWriter, r *http.Request) error {
	email, err := h.settings.Email(r.Context())
	if err != nil {
		return err
	}
	return httpx.JSON(w, r, settingsResponse{email})
}

// updateSettings decodes the body onto the stored document, so only the keys
// present change (null clears a string). The response carries the password,
// as the admin settings page expects.
func (h *Handler) updateSettings(w http.ResponseWriter, r *http.Request) error {
	body, err := httpx.Decode[json.RawMessage](w, r)
	if err != nil {
		return err
	}
	email, err := h.settings.UpdateEmail(r.Context(), func(email *settings.Email) error {
		if json.Unmarshal(body, email) != nil {
			return httpx.BadRequest("Invalid settings")
		}
		if email.Port < 1 || email.Port > 65535 {
			return httpx.BadRequest("Invalid port")
		}
		email.EreaderDevices = cleanDevices(email.EreaderDevices)
		return nil
	})
	if err != nil {
		return err
	}
	return httpx.JSON(w, r, settingsResponse{email})
}

// cleanDevices keeps the devices that have a name and an address and repairs
// availabilityOption (EmailSettings.update): an unknown value, or specificUsers
// with no users, becomes adminOrUp; only specificUsers keeps its users.
func cleanDevices(devices []settings.EreaderDevice) []settings.EreaderDevice {
	clean := make([]settings.EreaderDevice, 0, len(devices))
	for _, d := range devices {
		if d.Name == "" || d.Email == "" {
			continue
		}
		switch d.AvailabilityOption {
		case "userOrUp", "guestOrUp":
		case "specificUsers":
			if len(d.Users) > 0 {
				break
			}
			fallthrough
		default:
			d.AvailabilityOption = "adminOrUp"
		}
		if d.AvailabilityOption != "specificUsers" {
			d.Users = []string{}
		}
		clean = append(clean, d)
	}
	return clean
}

type devicesBody struct {
	EreaderDevices []settings.EreaderDevice `json:"ereaderDevices"`
}

// updateDevices replaces the whole device list and tells the admins.
func (h *Handler) updateDevices(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[devicesBody](w, r)
	if err != nil {
		return err
	}
	if req.EreaderDevices == nil {
		return httpx.BadRequest("Invalid payload. ereaderDevices array required")
	}
	for _, d := range req.EreaderDevices {
		if d.Name == "" || d.Email == "" {
			return httpx.BadRequest("Invalid payload. ereaderDevices array items must have name and email")
		}
	}
	email, err := h.settings.UpdateEmail(r.Context(), func(email *settings.Email) error {
		email.EreaderDevices = cleanDevices(req.EreaderDevices)
		return nil
	})
	if err != nil {
		return err
	}
	h.emit.ToAdmins("ereader-devices-updated", devicesBody{email.EreaderDevices})
	return httpx.JSON(w, r, devicesBody{email.EreaderDevices})
}

// serverOf is the SMTP server the settings describe. Like nodemailer
// (EmailSettings.getTransportObject), TLS from the first byte applies only to
// port 465.
func serverOf(e settings.Email) server {
	port := cmp.Or(int(e.Port), 465) // an unset port means 465
	return server{Host: db.Val(e.Host), Port: port, Implicit: e.Secure && port == 465, VerifyTLS: e.RejectUnauthorized,
		User: db.Val(e.User), Pass: db.Val(e.Pass)}
}

// sendTest sends "Success!" to the test address (else the from address). The
// admin sees the SMTP error text, as upstream.
func (h *Handler) sendTest(w http.ResponseWriter, r *http.Request) error {
	email, err := h.settings.Email(r.Context())
	if err != nil {
		return err
	}
	to := db.Val(email.TestAddress)
	if to == "" {
		to = db.Val(email.FromAddress)
	}
	err = send(r.Context(), serverOf(email), message{From: db.Val(email.FromAddress), To: to, Subject: "Test email from Audiobookshelf", Body: "Success!"})
	return mailResult(w, err, func(err error) string { return err.Error() })
}

// mailResult answers a send outcome: OK, the verify message for a connection
// failure, else 400 with describe(err).
func mailResult(w http.ResponseWriter, err error, describe func(error) string) error {
	var connect *connectError
	switch {
	case err == nil:
		return httpx.Text(w, http.StatusOK, "OK")
	case errors.As(err, &connect):
		slog.Error("SMTP connection failed", "error", err)
		return httpx.BadRequest(verifyFailed)
	}
	slog.Error("sending mail failed", "error", err)
	return httpx.BadRequest(describe(err))
}

// sendEbook mails the item's ebook to a device the user may use. SMTP detail
// is logged, never sent: any user may call this route.
func (h *Handler) sendEbook(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[struct{ LibraryItemID, DeviceName string }](w, r)
	if err != nil {
		return err
	}
	user := auth.User(r.Context())
	if req.LibraryItemID == "" || req.DeviceName == "" || user == nil {
		return httpx.BadRequest("libraryItemId and deviceName are required")
	}
	email, err := h.settings.Email(r.Context())
	if err != nil {
		return err
	}
	i := slices.IndexFunc(email.EreaderDevices, func(d settings.EreaderDevice) bool { return d.Name == req.DeviceName })
	if i < 0 {
		return httpx.Error(http.StatusNotFound, "Ereader device not found")
	}
	device := email.EreaderDevices[i]
	if !device.AllowedFor(user.ID, db.Val(user.Type)) {
		return httpx.Forbidden()
	}
	it, err := item.Get(r.Context(), h.db, req.LibraryItemID, item.Options{})
	if errors.Is(err, sql.ErrNoRows) {
		return httpx.Error(http.StatusNotFound, "Library item not found")
	} else if err != nil {
		return err
	}
	if a := it.Access(); !user.CanAccessItem(a.LibraryID, a.Explicit, a.Tags) {
		return httpx.Forbidden()
	}
	if it.Book == nil || it.Book.EbookFile.V == nil {
		return httpx.Error(http.StatusNotFound, "Ebook file not found")
	}
	file := it.Book.EbookFile.V.Metadata
	info, err := os.Stat(file.Path)
	if err != nil || !info.Mode().IsRegular() {
		return httpx.Error(http.StatusNotFound, "Ebook file not found")
	}
	if info.Size() > maxAttachmentBytes {
		return httpx.BadRequest("Ebook file is too large to email")
	}
	err = send(r.Context(), serverOf(email), message{From: db.Val(email.FromAddress), To: device.Email, Subject: "Here is your Ebook!", AttachPath: file.Path, AttachName: file.Filename})
	return mailResult(w, err, func(error) string { return "Failed to send ebook to device" })
}
