package auth

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/settings"
)

// claimSet is the userinfo document. It stays open-ended because the admin
// names the group and permission claims.
type claimSet map[string]json.RawMessage

// text is a string claim; absent or non-text claims read as "".
func (c claimSet) text(key string) string {
	var value string
	_ = json.Unmarshal(c[key], &value) // a claim of another type is not text
	return value
}

// flag is a boolean claim and whether it is present as one.
func (c claimSet) flag(key string) (value, present bool) {
	return value, json.Unmarshal(c[key], &value) == nil
}

// advancedPerms is the advanced-permissions claim (OidcAuthStrategy.js); any
// other property is an error.
type advancedPerms struct {
	CanDownload              *bool    `json:"canDownload"`
	CanUpload                *bool    `json:"canUpload"`
	CanDelete                *bool    `json:"canDelete"`
	CanUpdate                *bool    `json:"canUpdate"`
	CanAccessExplicitContent *bool    `json:"canAccessExplicitContent"`
	CanAccessAllLibraries    *bool    `json:"canAccessAllLibraries"`
	CanAccessAllTags         *bool    `json:"canAccessAllTags"`
	CanCreateEReader         *bool    `json:"canCreateEReader"`
	TagsAreDenylist          *bool    `json:"tagsAreDenylist"`
	AllowedLibraries         []string `json:"allowedLibraries"`
	AllowedTags              []string `json:"allowedTags"`
}

// oidcUser finds, links or registers the user the provider's claims describe
// and applies the group and permission claims, all in one transaction: a
// failure leaves nothing behind. A newly registered user is announced to the
// admins afterwards.
func (s *Service) oidcUser(ctx context.Context, server settings.Server, claims claimSet) (db.User, error) {
	var user db.User
	created := false
	err := s.db.Tx(ctx, func(ctx context.Context) (err error) {
		user, created, err = s.resolveOIDCUser(ctx, server, claims)
		return err
	})
	if err == nil && created {
		s.emit.ToAdmins("user_added", newUserView(user, nil))
	}
	return user, err
}

func (s *Service) resolveOIDCUser(ctx context.Context, server settings.Server, claims claimSet) (user db.User, created bool, err error) {
	sub := claims.text("sub")
	if sub == "" {
		return user, false, errors.New("invalid userinfo, no sub")
	}
	var groups []string
	if server.OpenIDGroupClaim != "" {
		if err := json.Unmarshal(claims[server.OpenIDGroupClaim], &groups); err != nil {
			return user, false, errors.New("OpenID group claim is missing or invalid")
		}
	}
	err = s.db.Get(ctx, &user, `SELECT * FROM users WHERE (CASE WHEN json_valid(extraData) THEN json_extract(extraData, '$.authOpenIDSub') END) = ? LIMIT 1`, sub)
	if errors.Is(err, sql.ErrNoRows) {
		if user, err = s.matchOIDCUser(ctx, server, claims); err == nil && user.ID != "" {
			err = s.linkOIDCUser(ctx, &user, sub)
		}
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return user, false, err
	}
	if user.ID == "" {
		if !server.OpenIDAutoRegister {
			return user, false, errors.New("user not found and auto-register is disabled")
		}
		if user, err = s.registerOIDCUser(ctx, claims, sub); err != nil {
			return user, false, err
		}
		created = true
	}
	if !user.IsActive {
		return user, false, errors.New("user not active")
	}
	if server.OpenIDGroupClaim != "" {
		if err = s.applyOIDCGroups(ctx, &user, groups); err != nil {
			return user, false, err
		}
	}
	if server.OpenIDAdvancedPermsClaim != "" && !NewPrincipal(user).IsAdmin() {
		err = s.applyOIDCPermissions(ctx, &user, claims[server.OpenIDAdvancedPermsClaim])
	}
	return user, created, err
}

// matchOIDCUser finds an existing user by the configured attribute; a zero
// user and no error means matching is off.
func (s *Service) matchOIDCUser(ctx context.Context, server settings.Server, claims claimSet) (db.User, error) {
	var user db.User
	switch server.OpenIDMatchExistingBy {
	case "email":
		email := claims.text("email")
		if email == "" {
			return user, errors.New("No email in userinfo")
		}
		if verified, present := claims.flag("email_verified"); present && !verified {
			return user, errors.New("Email not verified")
		}
		err := s.db.Get(ctx, &user, `SELECT * FROM users WHERE lower(email) = ? LIMIT 1`, strings.ToLower(email))
		return user, err
	case "username":
		username := claims.text("preferred_username")
		if username == "" {
			username = claims.text("username")
		}
		if username == "" {
			return user, errors.New("No username in userinfo")
		}
		return user, s.db.Get(ctx, &user, `SELECT * FROM users WHERE lower(username) = ? LIMIT 1`, strings.ToLower(username))
	}
	return user, nil
}

// linkOIDCUser records sub on a user matched by email or name; a user already
// linked to another subject cannot be taken over.
func (s *Service) linkOIDCUser(ctx context.Context, user *db.User, sub string) error {
	extra := db.UserExtra{}
	if user.ExtraData.V != nil {
		extra = *user.ExtraData.V
	}
	if db.Val(extra.AuthOpenIDSub) != "" {
		return errors.New("user already linked to a different OpenID subject")
	}
	if !user.IsActive {
		return nil // refused by the caller
	}
	extra.AuthOpenIDSub = &sub
	user.ExtraData.V, user.UpdatedAt = &extra, db.Now()
	return s.db.Update(ctx, "users", user, "extraData", "updatedAt")
}

// registerOIDCUser creates the user for claims: a plain "user" without a
// password, with the email only when the provider verified it.
func (s *Service) registerOIDCUser(ctx context.Context, claims claimSet, sub string) (db.User, error) {
	username := claims.text("preferred_username")
	if username == "" {
		username = claims.text("name")
	}
	if username == "" {
		username = sub
	}
	in := NewUser{Username: username, Type: "user", Active: true, OIDCSub: sub}
	if address := claims.text("email"); address != "" {
		if verified, _ := claims.flag("email_verified"); verified {
			in.Email = &address
		}
	}
	return s.CreateUser(ctx, in)
}

// applyOIDCGroups sets the user's role from the group claim: admin, user or
// guest, first match wins. Root stays root and cannot be mapped below admin.
func (s *Service) applyOIDCGroups(ctx context.Context, user *db.User, groups []string) error {
	role := ""
	for _, candidate := range []string{"admin", "user", "guest"} {
		for _, group := range groups {
			if strings.EqualFold(group, candidate) {
				role = candidate
			}
		}
		if role != "" {
			break
		}
	}
	switch {
	case role == "":
		return errors.New("No valid group found in userinfo")
	case db.Val(user.Type) == "root":
		if role != "admin" {
			return errors.New("Root user cannot be downgraded")
		}
		return nil
	case db.Val(user.Type) == role:
		return nil
	}
	user.Type, user.UpdatedAt = &role, db.Now()
	return s.db.Update(ctx, "users", user, "type", "updatedAt")
}

// applyOIDCPermissions applies the advanced-permissions claim to a non-admin
// user. Permissions the claim omits keep their value; the library and tag
// lists are cleared while "all" is granted and replaced only by a non-empty
// list.
func (s *Service) applyOIDCPermissions(ctx context.Context, user *db.User, raw json.RawMessage) error {
	var claim advancedPerms
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) == 0 || decoder.Decode(&claim) != nil {
		return fmt.Errorf("Advanced permissions claim is missing or invalid")
	}
	perms := &user.Permissions.V
	for dst, value := range map[*bool]*bool{
		&perms.Download: claim.CanDownload, &perms.Upload: claim.CanUpload, &perms.Delete: claim.CanDelete, &perms.Update: claim.CanUpdate,
		&perms.AccessExplicitContent: claim.CanAccessExplicitContent, &perms.AccessAllLibraries: claim.CanAccessAllLibraries,
		&perms.AccessAllTags: claim.CanAccessAllTags, &perms.CreateEreader: claim.CanCreateEReader, &perms.SelectedTagsNotAccessible: claim.TagsAreDenylist,
	} {
		if value != nil {
			*dst = *value
		}
	}
	switch {
	case perms.AccessAllLibraries:
		perms.LibrariesAccessible = []string{}
	case len(claim.AllowedLibraries) > 0:
		perms.LibrariesAccessible = claim.AllowedLibraries
	}
	switch {
	case perms.AccessAllTags:
		perms.ItemTagsSelected = []string{}
	case len(claim.AllowedTags) > 0:
		perms.ItemTagsSelected = claim.AllowedTags
	}
	user.UpdatedAt = db.Now()
	return s.db.Update(ctx, "users", user, "permissions", "updatedAt")
}
