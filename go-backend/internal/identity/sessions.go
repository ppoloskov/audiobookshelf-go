package identity

import (
	"context"

	"audiobookshelf-compat/internal/db/sqlc"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/users"
)

// UserSessions lists unexpired sessions, newest activity first.
func (h *Handler) UserSessions(ctx context.Context, userID, currentRefreshToken string, page, perPage int) (int, []users.SessionInfo, error) {
	now := dbtime.Now()
	total, err := h.queries().CountUserSessions(ctx, sqlc.CountUserSessionsParams{Userid: userID, Expiresat: now})
	if err != nil {
		return 0, nil, err
	}
	rows, err := h.queries().ListUserSessions(ctx, sqlc.ListUserSessionsParams{UserID: userID, Now: now, PageSize: int64(perPage), Skip: int64(perPage * page)})
	if err != nil {
		return 0, nil, err
	}
	sessions := make([]users.SessionInfo, 0, len(rows))
	for _, row := range rows {
		sessions = append(sessions, users.SessionInfo{
			ID: row.ID, IPAddress: store.NullStringPtr(row.Ipaddress), UserAgent: store.NullStringPtr(row.Useragent),
			CreatedAt: dbtime.Millis(row.Createdat), UpdatedAt: dbtime.Millis(row.Updatedat),
			Current: currentRefreshToken != "" && (currentRefreshToken == row.Refreshtoken || currentRefreshToken == row.Lastrefreshtoken.String),
		})
	}
	return int(total), sessions, nil
}

func (h *Handler) DeleteUserSession(ctx context.Context, userID, sessionID string) (bool, error) {
	res, err := h.queries().DeleteUserSession(ctx, sqlc.DeleteUserSessionParams{ID: sessionID, Userid: userID})
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
