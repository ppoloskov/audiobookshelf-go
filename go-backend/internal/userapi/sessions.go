package userapi

import (
	"database/sql"
	"net/http"
	"regexp"
	"strings"

	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/store"
)

// uuidPattern matches uuid.validate's canonical hexadecimal shape. UUID
// version and variant bits are not constrained (the all-zero UUID is valid).
var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (h *Handler) getSessions(w http.ResponseWriter, r *http.Request, user store.User) {
	page, perPage := queryInt(r, "page", 0), queryInt(r, "itemsPerPage", 10)
	if page < 0 {
		page = 0
	}
	if perPage < 1 {
		perPage = 1
	}
	if user.Type == "guest" {
		writeJSON(w, 200, map[string]any{"sessions": []any{}, "total": 0, "numPages": 0, "page": page, "itemsPerPage": perPage})
		return
	}
	var total int
	now := dbtime.Now()
	if err := h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM sessions WHERE userId=? AND expiresAt>?`, user.ID, now).Scan(&total); err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT id,ipAddress,userAgent,createdAt,updatedAt,refreshToken,lastRefreshToken FROM sessions WHERE userId=? AND expiresAt>? ORDER BY updatedAt DESC LIMIT ? OFFSET ?`, user.ID, now, perPage, perPage*page)
	if err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	defer rows.Close()
	refresh := refreshTokenFromRequest(r)
	sessions := make([]any, 0)
	for rows.Next() {
		var id string
		var ip, agent, token, last sql.NullString
		var created, updated any
		if err := rows.Scan(&id, &ip, &agent, &created, &updated, &token, &last); err != nil {
			http.Error(w, "database read failed", 500)
			return
		}
		sessions = append(sessions, map[string]any{"id": id, "ipAddress": nullableValue(ip), "userAgent": nullableValue(agent), "deviceInfo": parseUA(agent.String), "createdAt": millis(created), "updatedAt": millis(updated), "current": refresh != "" && (refresh == token.String || refresh == last.String)})
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "database read failed", 500)
		return
	}
	writeJSON(w, 200, map[string]any{"total": total, "numPages": int((total + perPage - 1) / perPage), "page": page, "itemsPerPage": perPage, "sessions": sessions})
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

func (h *Handler) deleteSession(w http.ResponseWriter, r *http.Request, user store.User, id string) {
	if user.Type == "guest" {
		sendStatus(w, 403)
		return
	}
	if !uuidPattern.MatchString(id) {
		sendStatus(w, 400)
		return
	}
	res, err := h.db.ExecContext(r.Context(), `DELETE FROM sessions WHERE id=? AND userId=?`, id, user.ID)
	if err != nil {
		http.Error(w, "database write failed", 500)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		sendStatus(w, 404)
		return
	}
	sendStatus(w, 200)
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
