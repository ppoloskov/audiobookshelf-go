package auth

import (
	"slices"

	"audiobookshelf-compat/internal/db"
)

// Principal is the authenticated user of a request: the users row, whose
// decoded permissions answer every permission question. Build one with
// NewPrincipal; a principal is a snapshot and is never mutated. A nil
// *Principal is safe to ask: every decision denies.
type Principal struct {
	db.User
}

// NewPrincipal wraps a users row. Malformed stored permissions have already
// decoded to the zero db.Permissions, which grants nothing (fail closed).
func NewPrincipal(u db.User) *Principal { return &Principal{User: u} }

// perms is the decoded permissions document; the zero value for nil.
func (p *Principal) perms() db.Permissions {
	if p == nil {
		return db.Permissions{}
	}
	return p.Permissions.V
}

// active reports an active user; false for nil.
func (p *Principal) active() bool { return p != nil && bool(p.IsActive) }

// IsRoot is the root user (type "root").
func (p *Principal) IsRoot() bool { return p != nil && db.Val(p.Type) == "root" }

// IsAdmin is root or admin (User.isAdminOrUp). It looks at the type alone.
func (p *Principal) IsAdmin() bool { return p.IsRoot() || (p != nil && db.Val(p.Type) == "admin") }

// CanUpdate is the update permission of an active user (User.canUpdate).
func (p *Principal) CanUpdate() bool { return p.active() && p.perms().Update }

// CanDelete is the delete permission of an active user (User.canDelete).
func (p *Principal) CanDelete() bool { return p.active() && p.perms().Delete }

// CanDownload is the download permission of an active user (User.canDownload).
func (p *Principal) CanDownload() bool { return p.active() && p.perms().Download }

// CanUpload is the upload permission of an active user (User.canUpload).
func (p *Principal) CanUpload() bool { return p.active() && p.perms().Upload }

// CanAccessLibrary is User.checkCanAccessLibrary: accessAllLibraries or a
// listed library. It has no admin bypass; admins hold accessAllLibraries.
func (p *Principal) CanAccessLibrary(libraryID string) bool {
	perms := p.perms()
	return p != nil && (perms.AccessAllLibraries || slices.Contains(perms.LibrariesAccessible, libraryID))
}

// CanListLibrary is the filter of LibraryController.findAll: only a non-empty
// librariesAccessible list restricts and accessAllLibraries is ignored, which
// differs from CanAccessLibrary on purpose (upstream does the same).
func (p *Principal) CanListLibrary(libraryID string) bool {
	perms := p.perms()
	return p != nil && (len(perms.LibrariesAccessible) == 0 || slices.Contains(perms.LibrariesAccessible, libraryID))
}

// CanAccessItem is User.checkCanAccessLibraryItem: the library rule plus
// CanAccessContent.
func (p *Principal) CanAccessItem(libraryID string, explicit bool, tags []string) bool {
	return p.CanAccessLibrary(libraryID) && p.CanAccessContent(explicit, tags)
}

// CanAccessContent is CanAccessItem without the library rule. Explicit media
// needs accessExplicitContent and an active user (User.canAccessExplicitContent).
// Tags (User.checkCanAccessLibraryItemWithTags): accessAllTags admits all;
// otherwise itemTagsSelected is an allow-list, or a deny-list with
// selectedTagsNotAccessible; an empty allow-list admits nothing. MediaClause
// must select exactly the media this admits.
func (p *Principal) CanAccessContent(explicit bool, tags []string) bool {
	if p == nil {
		return false
	}
	perms := p.perms()
	if explicit && !(perms.AccessExplicitContent && p.active()) {
		return false
	}
	if perms.AccessAllTags {
		return true
	}
	if len(tags) == 0 {
		return perms.SelectedTagsNotAccessible
	}
	shared := slices.ContainsFunc(tags, func(tag string) bool { return slices.Contains(perms.ItemTagsSelected, tag) })
	return shared != perms.SelectedTagsNotAccessible
}

// DefaultPermissions is the permissions document of a new user of userType
// (User.getDefaultPermissionsForUserType). Every creation path starts here.
// The lists are empty, non-nil slices so they encode as [].
func DefaultPermissions(userType string) db.Permissions {
	admin := userType == "root" || userType == "admin"
	return db.Permissions{
		Download: true, Update: admin, Delete: userType == "root", Upload: admin,
		CreateEreader: admin, AccessAllLibraries: true, AccessAllTags: true, AccessExplicitContent: admin,
		LibrariesAccessible: []string{}, ItemTagsSelected: []string{},
	}
}
