// Package access is the single owner of user permission decisions. A
// Permissions value is decoded once from a user row and answers every library,
// item, action and SQL-scope question (User.js:548-690).
package access

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"

	"audiobookshelf-compat/internal/httputil"
	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/store"
)

// Permissions is a user's decoded permissions object plus the row's type and
// active flag. Action flags stay untyped because upstream tests them for JS
// truthiness (`!!permissions.update`).
type Permissions struct {
	Type   string `json:"-"`
	Active bool   `json:"-"`

	AllLibraries bool     `json:"accessAllLibraries"`
	Libraries    []string `json:"librariesAccessible"`
	Explicit     bool     `json:"accessExplicitContent"`
	AllTags      bool     `json:"accessAllTags"`
	Tags         []string `json:"itemTagsSelected"`
	TagsExcluded bool     `json:"selectedTagsNotAccessible"`
	UpdateFlag   any      `json:"update"`
	DeleteFlag   any      `json:"delete"`
	DownloadFlag any      `json:"download"`
	UploadFlag   any      `json:"upload"`
}

// Of decodes the permissions of a user row. A document that does not decode
// completely (malformed JSON or a field of the wrong type) grants nothing:
// json.Unmarshal can fill earlier fields before failing on a later one.
func Of(u store.User) Permissions {
	p := Permissions{Type: u.Type, Active: u.IsActive}
	if json.Unmarshal(u.Permissions, &p) != nil {
		return Permissions{Type: u.Type, Active: u.IsActive}
	}
	return p
}

// Defaults is the permissions document of a new user of userType
// (User.getDefaultPermissionsForUserType). Every creation path starts here.
func Defaults(userType string) map[string]any {
	admin := userType == "root" || userType == "admin"
	return map[string]any{"download": true, "update": admin, "delete": userType == "root", "upload": admin, "createEreader": admin, "accessAllLibraries": true, "accessAllTags": true, "accessExplicitContent": admin, "selectedTagsNotAccessible": false, "librariesAccessible": []string{}, "itemTagsSelected": []string{}}
}

// Patch applies a client-supplied permissions patch to doc. Only boolean
// values for keys of Defaults(userType) are taken; the two list keys are taken
// from patch and then from top (a request's top-level keys, which win) when
// they are arrays of strings.
func Patch(doc map[string]any, userType string, patch, top map[string]any) {
	known := Defaults(userType)
	for key, value := range patch {
		if b, ok := value.(bool); ok && known[key] != nil {
			if _, isList := known[key].([]string); !isList {
				doc[key] = b
			}
		}
	}
	for _, src := range []map[string]any{patch, top} {
		for _, key := range []string{"librariesAccessible", "itemTagsSelected"} {
			if value, ok := src[key]; ok && validStringArray(value) {
				doc[key] = value
			}
		}
	}
}

func validStringArray(value any) bool {
	switch a := value.(type) {
	case []any:
		return !slices.ContainsFunc(a, func(v any) bool { _, ok := v.(string); return !ok })
	case []string:
		return true
	}
	return false
}

// IsAdmin is root or admin (User.isAdminOrUp).
func (p Permissions) IsAdmin() bool { return p.Type == "root" || p.Type == "admin" }

func (p Permissions) can(flag any) bool { return p.Active && jsonvalue.Truthy(flag) }

func (p Permissions) Update() bool   { return p.can(p.UpdateFlag) }
func (p Permissions) Delete() bool   { return p.can(p.DeleteFlag) }
func (p Permissions) Download() bool { return p.can(p.DownloadFlag) }
func (p Permissions) Upload() bool   { return p.can(p.UploadFlag) }

// Library is User.checkCanAccessLibrary; admins are the caller's concern.
func (p Permissions) Library(id string) bool {
	return p.AllLibraries || slices.Contains(p.Libraries, id)
}

// ListLibrary is LibraryController.findAll's filter: only a non-empty
// librariesAccessible list restricts, accessAllLibraries is ignored.
func (p Permissions) ListLibrary(id string) bool {
	return len(p.Libraries) == 0 || slices.Contains(p.Libraries, id)
}

// Item is User.checkCanAccessLibraryItem: library, explicit and tag rules.
func (p Permissions) Item(a libitem.Access) bool {
	return p.Library(a.LibraryID) && p.Content(a)
}

// ItemAccess loads the library item and answers whether it exists and whether
// the user may read it (User.checkCanAccessLibraryItem).
func ItemAccess(ctx context.Context, db *sql.DB, user store.User, itemID string) (exists, allowed bool, err error) {
	a, err := libitem.LoadAccess(ctx, db, itemID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return true, For(ctx, user).Item(a), nil
}

// CanReadItem is ItemAccess for callers that treat a missing item as refused.
func CanReadItem(ctx context.Context, db *sql.DB, user store.User, itemID string) (bool, error) {
	_, allowed, err := ItemAccess(ctx, db, user, itemID)
	return allowed, err
}

// Content is Item without the library rule (Access.LibraryID is ignored).
// Explicit content needs an active user (User.canAccessExplicitContent).
// MediaClause must select exactly the media Content allows
// (TestMediaClauseMatchesContent).
func (p Permissions) Content(a libitem.Access) bool {
	if a.Explicit && !(p.Explicit && p.Active) {
		return false
	}
	if p.AllTags {
		return true
	}
	if len(a.Tags) == 0 {
		return p.TagsExcluded
	}
	return slices.ContainsFunc(a.Tags, func(t string) bool { return slices.Contains(p.Tags, t) }) != p.TagsExcluded
}

// MediaClause is the AND clause hiding explicit media and media whose tags the
// user may not see, on the media row aliased alias (books b, podcasts p).
func (p Permissions) MediaClause(alias string) (string, []any) {
	clause, args := "", []any(nil)
	if !p.Explicit || !p.Active {
		clause += " AND " + alias + ".explicit=0"
	}
	if !p.AllTags && !p.TagsExcluded && len(p.Tags) == 0 {
		// An empty allow list admits no item (checkCanAccessLibraryItemWithTags).
		// Upstream's list queries skip the filter here and so disagree with its
		// own item check; we fail closed instead.
		clause += " AND 0"
	}
	if !p.AllTags && len(p.Tags) > 0 {
		compare := ">0"
		if p.TagsExcluded {
			compare = "=0"
		}
		clause += " AND (SELECT count(*) FROM json_each(" + alias + ".tags) WHERE json_each.value IN (" + store.Placeholders(len(p.Tags)) + "))" + compare
		for _, tag := range p.Tags {
			args = append(args, tag)
		}
	}
	return clause, args
}

// AdminUser is root or admin by the user row alone, without decoding permissions.
func AdminUser(u store.User) bool { return u.Type == "root" || u.Type == "admin" }

// RequireAdmin runs next for root and admin users and answers denied (an empty
// body, Express res.sendStatus) to anyone else. Most admin routes answer 403;
// the /api/emails ones answer 404 (EmailController.js adminMiddleware).
func RequireAdmin(denied int, next httputil.Handler) httputil.Handler {
	return func(w http.ResponseWriter, r *http.Request) error {
		if user, _ := store.UserFromContext(r.Context()); !AdminUser(user) {
			return httputil.Status(denied, "")
		}
		return next(w, r)
	}
}

type requestKey struct{}

type snapshot struct {
	user  store.User
	perms Permissions
}

// WithRequest decodes u once and carries the result in the request context.
// There is no cache beyond the request: the snapshot dies with the context.
func WithRequest(ctx context.Context, u store.User) context.Context {
	return context.WithValue(ctx, requestKey{}, snapshot{u, Of(u)})
}

// For is Of(u) that reuses the request's snapshot when u is the user the
// snapshot was decoded from (same row identity, type, state and permissions
// document); any other user, such as an admin route's target, decodes afresh.
func For(ctx context.Context, u store.User) Permissions {
	if s, ok := ctx.Value(requestKey{}).(snapshot); ok && s.user.ID == u.ID && s.user.Type == u.Type &&
		s.user.IsActive == u.IsActive && bytes.Equal(s.user.Permissions, u.Permissions) {
		return s.perms
	}
	return Of(u)
}
