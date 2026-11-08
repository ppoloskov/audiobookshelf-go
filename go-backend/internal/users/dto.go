// Package users is the shape of a user as clients receive it, shared by the
// identity and user-administration features.
package users

import (
	"encoding/json"

	"audiobookshelf-compat/internal/jsonvalue"
	"audiobookshelf-compat/internal/store"
	"audiobookshelf-compat/internal/wire"
)

// UserDTO is User.toOldJSONForBrowser: the user as every client receives it.
// The caller supplies the media progress overlay.
func UserDTO(user store.User, progress []wire.MediaProgress) map[string]any {
	if progress == nil {
		progress = []wire.MediaProgress{}
	}
	permissions := store.DecodeJSONObject(user.Permissions)
	// User.js:604-606/629-630: these two are always serialized as arrays.
	libraries := jsonvalue.Strings(permissions["librariesAccessible"])
	tags := jsonvalue.Strings(permissions["itemTagsSelected"])
	if libraries == nil {
		libraries = []string{}
	}
	if tags == nil {
		tags = []string{}
	}
	delete(permissions, "librariesAccessible")
	delete(permissions, "itemTagsSelected")
	extra := store.DecodeJSONObject(user.ExtraData)
	series := extra["seriesHideFromContinueListening"]
	if series == nil {
		series = []any{}
	}
	// users.email as stored: NULL is null, an empty string stays "" (User.js:614).
	var email, lastSeen, createdAt any
	if user.Email != nil {
		email = *user.Email
	}
	if user.LastSeen != nil {
		lastSeen = *user.LastSeen
	}
	if user.CreatedAt != nil {
		createdAt = *user.CreatedAt
	}
	return map[string]any{
		"id": user.ID, "username": user.Username, "email": email, "type": user.Type,
		"token": user.Token, "mediaProgress": progress, "seriesHideFromContinueListening": series,
		"bookmarks": store.DecodeJSONSlice[any](user.Bookmarks), "isActive": user.IsActive, "isLocked": user.IsLocked,
		"lastSeen": lastSeen, "createdAt": createdAt, "permissions": permissions,
		"librariesAccessible": libraries, "itemTagsSelected": tags,
		"hasOpenIDLink": jsonvalue.Truthy(extra["authOpenIDSub"]),
	}
}

// ListedUserDTO is the user list entry (GET /api/users): no progress or
// bookmarks, and the root token is blank unless a root user asks.
func ListedUserDTO(user store.User, hideRootToken bool) map[string]any {
	dto := UserDTO(user, nil)
	delete(dto, "mediaProgress")
	delete(dto, "bookmarks")
	if user.Type == "root" && hideRootToken {
		dto["token"] = ""
	}
	return dto
}

// NewUserExtraData is the extraData document of a new user; sub is the OpenID
// subject to link, or "".
func NewUserExtraData(sub string) string {
	extra := map[string]any{"seriesHideFromContinueListening": []string{}}
	if sub != "" {
		extra["authOpenIDSub"] = sub
	}
	data, _ := json.Marshal(extra) // plain map and slice: cannot fail
	return string(data)
}
