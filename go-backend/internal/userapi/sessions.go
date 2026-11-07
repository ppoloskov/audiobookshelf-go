package userapi

import (
	"database/sql"
	"net/http"
	"regexp"
	"strings"

	"audiobookshelf-compat/internal/dbtime"
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
	var total int
	now := dbtime.Now()
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM sessions WHERE userId=? AND expiresAt>?`, user.ID, now).Scan(&total); err != nil {
		return httputil.Internal("database read failed", err)
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT id,ipAddress,userAgent,createdAt,updatedAt,refreshToken,lastRefreshToken FROM sessions WHERE userId=? AND expiresAt>? ORDER BY updatedAt DESC LIMIT ? OFFSET ?`, user.ID, now, perPage, perPage*page)
	if err != nil {
		return httputil.Internal("database read failed", err)
	}
	defer rows.Close()
	refresh := refreshTokenFromRequest(r)
	sessions := make([]any, 0)
	for rows.Next() {
		var id string
		var ip, agent, token, last sql.NullString
		var created, updated any
		if err := rows.Scan(&id, &ip, &agent, &created, &updated, &token, &last); err != nil {
			return httputil.Internal("database read failed", err)
		}
		sessions = append(sessions, map[string]any{"id": id, "ipAddress": store.NullString(ip), "userAgent": store.NullString(agent), "deviceInfo": parseUA(agent.String), "createdAt": dbtime.Millis(created), "updatedAt": dbtime.Millis(updated), "current": refresh != "" && (refresh == token.String || refresh == last.String)})
	}
	if err := rows.Err(); err != nil {
		return httputil.Internal("database read failed", err)
	}
	return httputil.JSON(w, r, map[string]any{"total": total, "numPages": int((total + perPage - 1) / perPage), "page": page, "itemsPerPage": perPage, "sessions": sessions})
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
	res, err := h.db.ExecContext(r.Context(), `DELETE FROM sessions WHERE id=? AND userId=?`, id, user.ID)
	if err != nil {
		return httputil.Internal("database write failed", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return httputil.Status(404, "")
	}
	return httputil.OK(w, r)
}

func parseUA(agent string) map[string]any {
	if agent == "" {
		return nil
	}
	ua := strings.ToLower(agent)
	result := map[string]any{}
	switch {
	case strings.Contains(ua, "firefox/"):
		result["browserName"], result["browserVersion"] = "Firefox", uaVersion(agent, "Firefox/")
	case strings.Contains(ua, "edg/"):
		result["browserName"], result["browserVersion"] = "Edge", uaVersion(agent, "Edg/")
	case strings.Contains(ua, "chrome/"):
		result["browserName"], result["browserVersion"] = "Chrome", uaVersion(agent, "Chrome/")
	case strings.Contains(ua, "safari/"):
		result["browserName"], result["browserVersion"] = "Safari", uaVersion(agent, "Version/")
	}
	switch {
	case strings.Contains(ua, "windows nt"):
		result["osName"], result["osVersion"] = "Windows", uaVersion(agent, "Windows NT ")
	case strings.Contains(ua, "android"):
		result["osName"], result["osVersion"] = "Android", uaVersion(agent, "Android ")
	case strings.Contains(ua, "iphone") || strings.Contains(ua, "ipad"):
		result["osName"], result["osVersion"] = "iOS", uaVersion(agent, "OS ")
	case strings.Contains(ua, "mac os"):
		result["osName"], result["osVersion"] = "Mac OS", uaVersion(agent, "Mac OS X ")
	case strings.Contains(ua, "linux"):
		result["osName"] = "Linux"
	}
	if strings.Contains(ua, "mobile") || strings.Contains(ua, "iphone") || strings.Contains(ua, "android") {
		result["deviceType"] = "mobile"
	} else if strings.Contains(ua, "ipad") || strings.Contains(ua, "tablet") {
		result["deviceType"] = "tablet"
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func uaVersion(ua, token string) string {
	index := strings.Index(ua, token)
	if index < 0 {
		return ""
	}
	value := ua[index+len(token):]
	if end := strings.IndexAny(value, " ;)/"); end >= 0 {
		value = value[:end]
	}
	return strings.ReplaceAll(value, "_", ".")
}
