package notify

import (
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/settings"
)

// field tells an absent JSON key from null: Set is true when the key was present.
type field[T any] struct {
	Set   bool
	Value T
}

// UnmarshalJSON implements json.Unmarshaler; it runs only for a present key.
func (f *field[T]) UnmarshalJSON(data []byte) error {
	f.Set = true
	return json.Unmarshal(data, &f.Value)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	current, err := h.settings.Notifications(r.Context())
	if err != nil {
		return err
	}
	return httpx.JSON(w, r, map[string]any{"data": map[string]any{"events": catalogue}, "settings": current})
}

func (h *Handler) getData(w http.ResponseWriter, r *http.Request) error {
	return httpx.JSON(w, r, map[string]any{"events": catalogue})
}

// updateSettings sets the three editable settings; a missing key resets to its
// default (NotificationSettings.update), and an empty URL clears the Apprise URL.
func (h *Handler) updateSettings(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[struct {
		AppriseAPIURL        string `json:"appriseApiUrl"`
		MaxFailedAttempts    *int   `json:"maxFailedAttempts"`
		MaxNotificationQueue *int   `json:"maxNotificationQueue"`
	}](w, r)
	if err != nil {
		return err
	}
	_, err = h.settings.UpdateNotifications(r.Context(), func(s *settings.Notifications) error {
		s.AppriseAPIURL = nilIfEmpty(req.AppriseAPIURL)
		s.MaxFailedAttempts, s.MaxNotificationQueue = 5, 20
		set(&s.MaxFailedAttempts, req.MaxFailedAttempts)
		set(&s.MaxNotificationQueue, req.MaxNotificationQueue)
		return nil
	})
	if err != nil {
		return err
	}
	return httpx.Text(w, http.StatusOK, "OK")
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// create adds a notification. A request with no event name or no URLs adds
// nothing and answers the unchanged settings (NotificationSettings.createNotification).
func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	req, err := httpx.Decode[struct {
		LibraryID     string   `json:"libraryId"`
		EventName     string   `json:"eventName"`
		URLs          []string `json:"urls"`
		TitleTemplate string   `json:"titleTemplate"`
		BodyTemplate  string   `json:"bodyTemplate"`
		Enabled       bool     `json:"enabled"`
		Type          string   `json:"type"`
	}](w, r)
	if err != nil {
		return err
	}
	var updated settings.Notifications
	if req.EventName == "" || len(req.URLs) == 0 {
		updated, err = h.settings.Notifications(r.Context())
	} else {
		updated, err = h.settings.UpdateNotifications(r.Context(), func(s *settings.Notifications) error {
			s.Notifications = append(s.Notifications, settings.Notification{
				ID: ids.Must(), LibraryID: nilIfEmpty(req.LibraryID), EventName: req.EventName, URLs: req.URLs,
				TitleTemplate: req.TitleTemplate, BodyTemplate: req.BodyTemplate, Enabled: req.Enabled,
				Type: nilIfEmpty(req.Type), CreatedAt: time.Now().UnixMilli()})
			return nil
		})
	}
	if err != nil {
		return err
	}
	return httpx.JSON(w, r, updated)
}

// remove deletes the notification named in the path.
func (h *Handler) remove(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	updated, err := h.settings.UpdateNotifications(r.Context(), func(s *settings.Notifications) error {
		if s.Find(id) == nil {
			return httpx.NotFound()
		}
		s.Notifications = slices.DeleteFunc(s.Notifications, func(n settings.Notification) bool { return n.ID == id })
		return nil
	})
	if err != nil {
		return err
	}
	return httpx.JSON(w, r, updated)
}

// update patches the notification named in the path (the route id, not a body
// id; 404 for an unknown id comes after the body check). Enabling a disabled notification resets its failure record
// (Notification.update).
func (h *Handler) update(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	req, err := httpx.Decode[struct {
		LibraryID     field[*string] `json:"libraryId"`
		EventName     *string        `json:"eventName"`
		URLs          *[]string      `json:"urls"`
		TitleTemplate *string        `json:"titleTemplate"`
		BodyTemplate  *string        `json:"bodyTemplate"`
		Enabled       *bool          `json:"enabled"`
		Type          field[*string] `json:"type"`
	}](w, r)
	if err != nil {
		return err
	}
	updated, err := h.settings.UpdateNotifications(r.Context(), func(s *settings.Notifications) error {
		n := s.Find(id)
		if n == nil {
			return httpx.NotFound()
		}
		if req.Enabled != nil && *req.Enabled && !n.Enabled {
			n.LastFiredAt, n.LastAttemptFailed, n.NumConsecutiveFailedAttempts = nil, false, 0
		}
		set(&n.EventName, req.EventName)
		set(&n.TitleTemplate, req.TitleTemplate)
		set(&n.BodyTemplate, req.BodyTemplate)
		set(&n.Enabled, req.Enabled)
		set(&n.URLs, req.URLs)
		if req.LibraryID.Set {
			n.LibraryID = req.LibraryID.Value
		}
		if req.Type.Set {
			n.Type = req.Type.Value
		}
		return nil
	})
	if err != nil {
		return err
	}
	return httpx.JSON(w, r, updated)
}

func set[T any](dst *T, src *T) {
	if src != nil {
		*dst = *src
	}
}

// testEvent fires the onTest event (GET /api/notifications/test). It answers
// 200 whatever the delivery outcome; ?fail=1 records a failed attempt without sending.
func (h *Handler) testEvent(w http.ResponseWriter, r *http.Request) error {
	test, _ := eventNamed("onTest")
	err := h.deliver(r.Context(), test.Name, test.TestData, r.URL.Query().Get("fail") == "1")
	if err != nil {
		return err
	}
	return httpx.Text(w, http.StatusOK, "OK")
}

// testNotification sends one notification with its event's sample data.
func (h *Handler) testNotification(w http.ResponseWriter, r *http.Request) error {
	current, err := h.settings.Notifications(r.Context())
	if err != nil {
		return err
	}
	n := current.Find(r.PathValue("id"))
	if n == nil {
		return httpx.NotFound()
	}
	if !current.IsUseable() {
		return httpx.BadRequest("Apprise is not configured")
	}
	event, _ := eventNamed(n.EventName) // an event not in the catalogue is sent with no sample data
	if err := h.post(r.Context(), *current.AppriseAPIURL, payloadFor(*n, event.TestData)); err != nil {
		return err
	}
	return httpx.Text(w, http.StatusOK, "OK")
}
