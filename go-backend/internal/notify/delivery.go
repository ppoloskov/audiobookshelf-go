package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"audiobookshelf-compat/internal/settings"
)

// apprisePayload is the body posted to the Apprise API.
type apprisePayload struct {
	URLs  []string `json:"urls"`
	Title string   `json:"title"`
	Body  string   `json:"body"`
}

// TriggerEvent delivers the event to every enabled notification for it and
// records the outcomes. Unknown events, an unset Apprise URL and an event with
// no enabled notification do nothing. Delivery is synchronous. Podcasts and
// backup call it as their notification hook.
func (h *Handler) TriggerEvent(ctx context.Context, eventName string, data map[string]any) error {
	if _, ok := eventNamed(eventName); !ok {
		return nil
	}
	return h.deliver(ctx, eventName, data, false)
}

// deliver posts the event once per enabled notification (with fail set it only
// records failures), saves the outcomes by id and emits notifications_updated.
// The posts run outside the settings transaction.
func (h *Handler) deliver(ctx context.Context, eventName string, data map[string]any, fail bool) error {
	cfg, err := h.settings.Notifications(ctx)
	if err != nil {
		return err
	}
	active := cfg.Active(eventName)
	if !cfg.IsUseable() || len(active) == 0 {
		return nil
	}
	sent := make(map[string]bool, len(active)) // notification id -> delivered
	for _, n := range active {
		sent[n.ID] = false
		if fail {
			continue // recorded as failed without sending
		}
		err := h.post(ctx, *cfg.AppriseAPIURL, payloadFor(n, data))
		if err != nil {
			slog.Error("notification delivery failed", "event", eventName, "notification", n.ID, "error", err)
		}
		sent[n.ID] = err == nil
	}
	updated, err := h.settings.UpdateNotifications(ctx, func(current *settings.Notifications) error {
		now := time.Now().UnixMilli()
		for id, ok := range sent {
			n := current.Find(id)
			if n == nil {
				continue
			}
			n.LastFiredAt, n.LastAttemptFailed = &now, !ok
			n.NumTimesFired++
			if ok {
				n.NumConsecutiveFailedAttempts = 0
			} else if n.NumConsecutiveFailedAttempts++; n.NumConsecutiveFailedAttempts >= current.MaxFailedAttempts {
				n.Enabled = false
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	h.emit.ToAll("notifications_updated", updated)
	return nil
}

// post sends one payload to the Apprise API.
func (h *Handler) post(ctx context.Context, endpoint string, payload apprisePayload) error {
	body, _ := json.Marshal(payload) // strings and a string list cannot fail
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("apprise answered %d", res.StatusCode)
	}
	return nil
}

func payloadFor(n settings.Notification, data map[string]any) apprisePayload {
	return apprisePayload{n.URLs, render(n.TitleTemplate, data), render(n.BodyTemplate, data)}
}

var placeholder = regexp.MustCompile(`{{ ?([a-zA-Z]+) ?}}`)

// render replaces {{name}} and {{ name }} with the data value. Like
// Notification.replaceVariablesInTemplate, a missing or falsy value (nil, "",
// false, 0) leaves the placeholder text in place.
func render(template string, data map[string]any) string {
	return placeholder.ReplaceAllStringFunc(template, func(match string) string {
		switch v := data[placeholder.FindStringSubmatch(match)[1]]; v {
		case nil, "", false, 0, 0.0:
			return match
		default:
			return fmt.Sprint(v)
		}
	})
}
