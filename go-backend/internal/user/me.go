package user

import (
	"log/slog"
	"net/http"
	"slices"

	"audiobookshelf-compat/internal/auth"
	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/deviceinfo"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/ids"
	"audiobookshelf-compat/internal/playback"
	"audiobookshelf-compat/internal/settings"
)

func (h *Handler) getMe(w http.ResponseWriter, r *http.Request, me *auth.Principal) error {
	view, err := h.auth.ViewWithProgress(r.Context(), me.User)
	if err != nil {
		return err
	}
	return httpx.JSON(w, r, view)
}

// passwordRequest is the body of PATCH /api/me/password; both keys must be sent, null reads as "".
type passwordRequest struct {
	Password    playback.Field[string] `json:"password"`
	NewPassword playback.Field[string] `json:"newPassword"`
}

// changePassword is PATCH /api/me/password (not for guests); other sessions end, this one rotates.
func (h *Handler) changePassword(w http.ResponseWriter, r *http.Request, me *auth.Principal) error {
	if db.Val(me.Type) == "guest" {
		return httpx.Forbidden()
	}
	req, err := httpx.Decode[passwordRequest](w, r)
	if err != nil || !req.Password.Present || !req.NewPassword.Present {
		return httpx.BadRequest("Missing or invalid password or new password")
	}
	ctx := r.Context()
	if err := h.auth.ChangePassword(ctx, me.ID, req.Password.Value, req.NewPassword.Value); err != nil {
		slog.Error("failed to change password", "username", db.Val(me.Username), "error", err)
		return err
	}
	slog.Info("user changed password", "username", db.Val(me.Username))
	access, refresh, err := h.auth.InvalidateUserSessions(ctx, me.ID, refreshToken(r))
	if err != nil {
		return err
	}
	switch {
	case refresh == "":
	case r.Header.Get("x-refresh-token") != "": // mobile apps keep the token themselves
		return httpx.JSON(w, r, map[string]any{"success": true, "user": map[string]string{"accessToken": access, "refreshToken": refresh}})
	default:
		h.auth.SetRefreshTokenCookie(w, r, refresh)
	}
	return ok(w)
}

func refreshToken(r *http.Request) string {
	if token := r.Header.Get("x-refresh-token"); token != "" {
		return token
	}
	if cookie, err := r.Cookie("refresh_token"); err == nil {
		return cookie.Value
	}
	return ""
}

type sessionView struct {
	ID         string           `json:"id"`
	IPAddress  *string          `json:"ipAddress"`
	UserAgent  *string          `json:"userAgent"`
	DeviceInfo *deviceinfo.Info `json:"deviceInfo"` // null when the User-Agent says nothing
	CreatedAt  db.Time          `json:"createdAt"`
	UpdatedAt  db.Time          `json:"updatedAt"`
	Current    bool             `json:"current"` // the session of this request
}

func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request, me *auth.Principal) error {
	page, perPage := pageOf(r)
	perPage = max(perPage, 1)
	sessions, total := []sessionView{}, 0
	if db.Val(me.Type) != "guest" {
		count, rows, err := h.auth.Sessions(r.Context(), me.ID, page, perPage)
		if err != nil {
			return err
		}
		total = count
		current := refreshToken(r)
		for _, s := range rows {
			view := sessionView{ID: s.ID, IPAddress: s.IPAddress, UserAgent: s.UserAgent, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
				Current: current != "" && (current == s.RefreshToken || current == db.Val(s.LastRefreshToken))}
			if info := deviceinfo.Parse(db.Val(s.UserAgent)); info != (deviceinfo.Info{}) {
				view.DeviceInfo = &info
			}
			sessions = append(sessions, view)
		}
	}
	return httpx.JSON(w, r, sessionsPage[sessionView]{total, (total + perPage - 1) / perPage, page, perPage, sessions})
}

// deleteSession is DELETE /api/me/sessions/{id}: one of the user's own sessions (not for guests).
func (h *Handler) deleteSession(w http.ResponseWriter, r *http.Request, me *auth.Principal) error {
	if db.Val(me.Type) == "guest" {
		return httpx.Forbidden()
	}
	id := r.PathValue("id")
	if !ids.Valid(id) {
		return httpx.Error(http.StatusBadRequest, "")
	}
	found, err := h.auth.DeleteSession(r.Context(), me.ID, id)
	if err != nil {
		return err
	}
	if !found {
		return httpx.NotFound()
	}
	return ok(w)
}

// ereaderDevicesRequest is the body of POST /api/me/ereader-devices; nil is absent or null.
type ereaderDevicesRequest struct {
	Devices *[]settings.EreaderDevice `json:"ereaderDevices"`
}

// updateEreaderDevices is POST /api/me/ereader-devices: it replaces the devices only this user may use.
func (h *Handler) updateEreaderDevices(w http.ResponseWriter, r *http.Request, me *auth.Principal) error {
	req, err := httpx.Decode[ereaderDevicesRequest](w, r)
	if err != nil || req.Devices == nil {
		return httpx.BadRequest("Invalid payload. ereaderDevices array required")
	}
	for _, d := range *req.Devices {
		if d.Name == "" || d.Email == "" || d.AvailabilityOption != "specificUsers" || !slices.Equal(d.Users, []string{me.ID}) {
			return httpx.BadRequest(`Invalid payload. ereaderDevices items must have name, email, availabilityOption "specificUsers" and only the current user`)
		}
	}
	userType := db.Val(me.Type)
	email, err := h.settings.UpdateEmail(r.Context(), func(email *settings.Email) error {
		kept := slices.DeleteFunc(email.EreaderDevices, func(d settings.EreaderDevice) bool {
			return d.AllowedFor(me.ID, userType) && len(d.Users) == 1
		})
		email.EreaderDevices = append(kept, *req.Devices...)
		names := map[string]bool{}
		for _, d := range email.EreaderDevices {
			if names[d.Name] {
				return httpx.BadRequest(`Invalid payload. Duplicate "name" field found.`)
			}
			names[d.Name] = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	visible := wrap("ereaderDevices", email.DevicesFor(me.ID, userType))
	h.emit.ToUser(me.ID, "ereader-devices-updated", visible)
	return httpx.JSON(w, r, visible)
}
