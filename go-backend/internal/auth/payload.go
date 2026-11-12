package auth

import (
	"context"
	"fmt"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/settings"
	"audiobookshelf-compat/internal/textclean"
)

// authPayload is what login, refresh, authorize and the OpenID mobile callback
// answer: the user, the server settings a browser may see and the e-reader
// devices the user may send to.
type authPayload[U any] struct {
	User                 U                        `json:"user"`
	UserDefaultLibraryID *string                  `json:"userDefaultLibraryId"`
	ServerSettings       settings.ServerPublic    `json:"serverSettings"`
	EreaderDevices       []settings.EreaderDevice `json:"ereaderDevices"`
	Source               string                   `json:"Source"`
}

// sessionUser is the user of a login or refresh answer: the tokens come with
// it. A null refreshToken means the cookie carries it.
type sessionUser struct {
	userView
	AccessToken  string  `json:"accessToken"`
	RefreshToken *string `json:"refreshToken"`
}

// signedIn adds the tokens of a new or refreshed session to payload.
func signedIn(payload authPayload[userView], access string, refresh *string) authPayload[sessionUser] {
	return authPayload[sessionUser]{
		User: sessionUser{payload.User, access, refresh}, UserDefaultLibraryID: payload.UserDefaultLibraryID,
		ServerSettings: payload.ServerSettings, EreaderDevices: payload.EreaderDevices, Source: payload.Source,
	}
}

// userView is the user as every client receives it (User.toOldJSONForBrowser).
type userView struct {
	ID                              string          `json:"id"`
	Username                        *string         `json:"username"`
	Email                           *string         `json:"email"`
	Type                            *string         `json:"type"`
	Token                           *string         `json:"token"`
	MediaProgress                   []progressView  `json:"mediaProgress"`
	SeriesHideFromContinueListening []string        `json:"seriesHideFromContinueListening"`
	Bookmarks                       []db.Bookmark   `json:"bookmarks"`
	IsActive                        db.Bool         `json:"isActive"`
	IsLocked                        db.Bool         `json:"isLocked"`
	LastSeen                        db.Time         `json:"lastSeen"`
	CreatedAt                       db.Time         `json:"createdAt"`
	Permissions                     permissionsView `json:"permissions"`
	LibrariesAccessible             []string        `json:"librariesAccessible"`
	ItemTagsSelected                []string        `json:"itemTagsSelected"`
	HasOpenIDLink                   bool            `json:"hasOpenIDLink"`
}

// permissionsView is the permissions object; the two lists travel beside it
// in userView.
type permissionsView struct {
	Download                  bool `json:"download"`
	Update                    bool `json:"update"`
	Delete                    bool `json:"delete"`
	Upload                    bool `json:"upload"`
	CreateEreader             bool `json:"createEreader"`
	AccessAllLibraries        bool `json:"accessAllLibraries"`
	AccessAllTags             bool `json:"accessAllTags"`
	AccessExplicitContent     bool `json:"accessExplicitContent"`
	SelectedTagsNotAccessible bool `json:"selectedTagsNotAccessible"`
}

// progressView is one media progress as the legacy clients read it
// (MediaProgress.getOldMediaProgress).
type progressView struct {
	ID                        string   `json:"id"`
	UserID                    string   `json:"userId"`
	LibraryItemID             *string  `json:"libraryItemId"`
	EpisodeID                 *string  `json:"episodeId"`
	MediaItemID               string   `json:"mediaItemId"`
	MediaItemType             string   `json:"mediaItemType"`
	Duration                  *float64 `json:"duration"`
	Progress                  float64  `json:"progress"`
	CurrentTime               float64  `json:"currentTime"`
	IsFinished                bool     `json:"isFinished"`
	HideFromContinueListening bool     `json:"hideFromContinueListening"`
	EbookLocation             *string  `json:"ebookLocation"`
	EbookProgress             *float64 `json:"ebookProgress"`
	LastUpdate                db.Time  `json:"lastUpdate"`
	StartedAt                 db.Time  `json:"startedAt"`
	FinishedAt                db.Time  `json:"finishedAt"`
}

// newUserView builds the user document of user with its media progress.
func newUserView(user db.User, progress []db.MediaProgress) userView {
	perms := user.Permissions.V
	extra := db.UserExtra{}
	if user.ExtraData.V != nil {
		extra = *user.ExtraData.V
	}
	view := userView{
		ID: user.ID, Username: user.Username, Email: user.Email, Type: user.Type, Token: user.Token,
		MediaProgress: []progressView{}, SeriesHideFromContinueListening: extra.SeriesHideFromContinueListening,
		Bookmarks: user.Bookmarks.V, IsActive: user.IsActive, IsLocked: user.IsLocked, LastSeen: user.LastSeen, CreatedAt: user.CreatedAt,
		Permissions: permissionsView{perms.Download, perms.Update, perms.Delete, perms.Upload, perms.CreateEreader,
			perms.AccessAllLibraries, perms.AccessAllTags, perms.AccessExplicitContent, perms.SelectedTagsNotAccessible},
		LibrariesAccessible: perms.LibrariesAccessible, ItemTagsSelected: perms.ItemTagsSelected,
		HasOpenIDLink: db.Val(extra.AuthOpenIDSub) != "",
	}
	for _, list := range []*[]string{&view.SeriesHideFromContinueListening, &view.LibrariesAccessible, &view.ItemTagsSelected} {
		if *list == nil {
			*list = []string{} // clients expect arrays
		}
	}
	if view.Bookmarks == nil {
		view.Bookmarks = []db.Bookmark{}
	}
	for _, p := range progress {
		v := progressView{
			ID: p.ID, UserID: db.Val(p.UserID), MediaItemID: db.Val(p.MediaItemID), MediaItemType: db.Val(p.MediaItemType),
			Duration: p.Duration, CurrentTime: db.Val(p.CurrentTime), IsFinished: bool(p.IsFinished),
			HideFromContinueListening: bool(p.HideFromContinueListening), EbookLocation: p.EbookLocation, EbookProgress: p.EbookProgress,
			LastUpdate: p.UpdatedAt, StartedAt: p.CreatedAt, FinishedAt: p.FinishedAt,
		}
		if extra := p.ExtraData.V; extra != nil {
			v.Progress = db.Val(extra.Progress)
			if id := db.Val(extra.LibraryItemID); id != "" {
				v.LibraryItemID = &id
			}
		}
		if v.MediaItemType == "podcastEpisode" {
			v.EpisodeID = &v.MediaItemID
		}
		view.MediaProgress = append(view.MediaProgress, v)
	}
	return view
}

// payloadFor loads everything the login-style answers carry for user.
func (s *Service) payloadFor(ctx context.Context, user db.User) (authPayload[userView], error) {
	progress, err := s.progressOf(ctx, user.ID)
	if err != nil {
		return authPayload[userView]{}, err
	}
	var libraryIDs []string
	if err := s.db.Select(ctx, &libraryIDs, `SELECT id FROM libraries ORDER BY displayOrder ASC`); err != nil {
		return authPayload[userView]{}, fmt.Errorf("load libraries: %w", err)
	}
	email, err := s.settings.Email(ctx)
	if err != nil {
		return authPayload[userView]{}, err
	}
	principal := NewPrincipal(user)
	payload := authPayload[userView]{
		User: newUserView(user, progress), ServerSettings: s.settings.Server().Public(),
		EreaderDevices: email.DevicesFor(user.ID, db.Val(user.Type)), Source: s.cfg.Source,
	}
	for _, id := range libraryIDs {
		if principal.CanAccessLibrary(id) {
			payload.UserDefaultLibraryID = &id
			break
		}
	}
	payload.ServerSettings.LoginCustomMessage = settings.NullString(textclean.Sanitize(string(payload.ServerSettings.LoginCustomMessage)))
	return payload, nil
}

// progressOf reads the media progress rows of a user in the order upstream
// loads them.
func (s *Service) progressOf(ctx context.Context, userID string) ([]db.MediaProgress, error) {
	var progress []db.MediaProgress
	if err := s.db.Select(ctx, &progress, `SELECT * FROM mediaProgresses WHERE userId = ? ORDER BY mediaItemId, isFinished, currentTime`, userID); err != nil {
		return nil, fmt.Errorf("load media progress: %w", err)
	}
	return progress, nil
}

// UserView is a user as clients receive it (User.toOldJSONForBrowser).
type UserView = userView

// ViewOf is the view of user with an empty mediaProgress.
func ViewOf(user db.User) UserView { return newUserView(user, nil) }

// ViewWithProgress is the view of user with its media progress.
func (s *Service) ViewWithProgress(ctx context.Context, user db.User) (UserView, error) {
	progress, err := s.progressOf(ctx, user.ID)
	if err != nil {
		return UserView{}, err
	}
	return newUserView(user, progress), nil
}
