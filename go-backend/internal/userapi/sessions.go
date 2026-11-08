package userapi

import (
	"net/http"
	"regexp"

	"audiobookshelf-compat/internal/deviceinfo"
	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/store"
)

// uuidPattern matches uuid.validate's canonical hexadecimal shape. UUID
// version and variant bits are not constrained (the all-zero UUID is valid).
var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (h *Handler) getSessions(w http.ResponseWriter, r *http.Request, user store.User) error {
	page, perPage := queryInt(r, "page", 0), queryInt(r, "itemsPerPage", 10)
	if page < 0 {
		page = 0
	}
	if perPage < 1 {
		perPage = 1
	}
	if user.Type == "guest" {
		return httputil.JSON(w, r, map[string]any{"sessions": []any{}, "total": 0, "numPages": 0, "page": page, "itemsPerPage": perPage})
	}
	total, found, err := h.ident.UserSessions(r.Context(), user.ID, refreshTokenFromRequest(r), page, perPage)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	sessions := make([]any, 0, len(found))
	for _, s := range found {
		sessions = append(sessions, map[string]any{"id": s.ID, "ipAddress": s.IPAddress, "userAgent": s.UserAgent, "deviceInfo": deviceInfo(derefString(s.UserAgent)), "createdAt": s.CreatedAt, "updatedAt": s.UpdatedAt, "current": s.Current})
	}
	return httputil.JSON(w, r, map[string]any{"total": total, "numPages": (total + perPage - 1) / perPage, "page": page, "itemsPerPage": perPage, "sessions": sessions})
}

func refreshTokenFromRequest(r *http.Request) string {
	if token := r.Header.Get("x-refresh-token"); token != "" {
		return token
	}
	if c, err := r.Cookie("refresh_token"); err == nil {
		return c.Value
	}
	return ""
}

func (h *Handler) deleteSession(w http.ResponseWriter, r *http.Request, user store.User) error {
	id := r.PathValue("id")
	if user.Type == "guest" {
		return httputil.Status(403, "")
	}
	if !uuidPattern.MatchString(id) {
		return httputil.Status(400, "")
	}
	found, err := h.ident.DeleteUserSession(r.Context(), user.ID, id)
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	if !found {
		return httputil.Status(404, "")
	}
	return httputil.OK(w, r)
}

// deviceInfo is the client description of a session (parseUserAgent.js); keys
// of unrecognised parts are absent and a User-Agent with none yields null.
func deviceInfo(agent string) map[string]any {
	if agent == "" {
		return nil
	}
	info := deviceinfo.Parse(agent)
	result := map[string]any{}
	for key, value := range map[string]string{"browserName": info.Browser, "browserVersion": info.BrowserVersion, "osName": info.OSName, "osVersion": info.OSVersion, "deviceType": info.DeviceType} {
		if value != "" {
			result[key] = value
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}
