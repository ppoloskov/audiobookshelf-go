package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/ids"
)

// NewUser is what the ways of creating a user (first-run root, admin form,
// OpenID auto-registration) differ in; CreateUser fills in the rest.
type NewUser struct {
	Username     string
	Email        *string // nil: NULL
	PasswordHash *string // nil: NULL (OpenID users have no password)
	Type         string
	Active       bool
	Permissions  *db.Permissions // nil: the defaults of Type
	OIDCSub      string          // linked OpenID subject, or ""
}

// CreateUser inserts a user and returns its row. Its permanent token
// (users.token) is signed here.
func (s *Service) CreateUser(ctx context.Context, in NewUser) (db.User, error) {
	id, err := ids.New()
	if err != nil {
		return db.User{}, fmt.Errorf("user id: %w", err)
	}
	token, err := s.legacyToken(id, in.Username)
	if err != nil {
		return db.User{}, fmt.Errorf("user token: %w", err)
	}
	permissions := DefaultPermissions(in.Type)
	if in.Permissions != nil {
		permissions = *in.Permissions
	}
	extra := db.UserExtra{SeriesHideFromContinueListening: []string{}}
	if in.OIDCSub != "" {
		extra.AuthOpenIDSub = &in.OIDCSub
	}
	now := db.Now()
	user := db.User{
		ID: id, Username: &in.Username, Email: in.Email, Pash: in.PasswordHash, Type: &in.Type, Token: &token,
		IsActive: db.Bool(in.Active), Permissions: db.JSON[db.Permissions]{V: permissions},
		Bookmarks: db.JSON[[]db.Bookmark]{V: []db.Bookmark{}}, ExtraData: db.JSON[*db.UserExtra]{V: &extra},
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.db.Insert(ctx, "users", &user); err != nil {
		return db.User{}, err
	}
	return user, nil
}

// userByID loads a user; a missing one is sql.ErrNoRows.
func (s *Service) userByID(ctx context.Context, id string) (db.User, error) {
	var user db.User
	err := s.db.Get(ctx, &user, `SELECT * FROM users WHERE id = ?`, id)
	return user, err
}

// userByIDOrOldID finds the user a token names. Tokens minted before the 2.3
// id migration carry the old id, kept in extraData.oldUserId.
func (s *Service) userByIDOrOldID(ctx context.Context, id string) (db.User, error) {
	user, err := s.userByID(ctx, id)
	if !errors.Is(err, sql.ErrNoRows) {
		return user, err
	}
	err = s.db.Get(ctx, &user, `SELECT * FROM users WHERE (CASE WHEN json_valid(extraData) THEN json_extract(extraData, '$.oldUserId') END) = ? LIMIT 1`, id)
	return user, err
}

// userByUsername finds a user by login name, ignoring case.
func (s *Service) userByUsername(ctx context.Context, username string) (db.User, error) {
	var user db.User
	err := s.db.Get(ctx, &user, `SELECT * FROM users WHERE lower(username) = ? LIMIT 1`, strings.ToLower(username))
	return user, err
}
