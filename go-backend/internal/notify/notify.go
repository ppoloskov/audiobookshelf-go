// Package notify owns the notification settings routes, the event catalogue
// and the delivery of events to an Apprise API.
package notify

import (
	"net/http"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/events"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/settings"
)

// Handler serves the notification routes and delivers notification events.
type Handler struct {
	settings *settings.Store
	emit     events.Emitter
}

// New returns a Handler over the notification settings in store.
func New(store *settings.Store, emit events.Emitter) *Handler {
	return &Handler{settings: store, emit: emit}
}

// Routes registers the notification routes, all admin only (403 for others).
func (h *Handler) Routes(mux *http.ServeMux) {
	admin := func(pattern string, h httpx.Handler) { mux.Handle(pattern, auth.Admin(h)) }
	admin("GET /api/notifications", h.get)
	admin("PATCH /api/notifications", h.updateSettings)
	admin("POST /api/notifications", h.create)
	admin("GET /api/notificationdata", h.getData)
	admin("GET /api/notifications/test", h.testEvent)
	admin("GET /api/notifications/{id}/test", h.testNotification)
	admin("DELETE /api/notifications/{id}", h.remove)
	admin("PATCH /api/notifications/{id}", h.update)
}
