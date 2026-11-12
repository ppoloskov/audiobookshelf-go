package admin

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/httpx"
	"audiobookshelf-compat/internal/settings"
	"audiobookshelf-compat/internal/text"
)

// errUnchanged ends an update whose patch changed nothing: nothing is written
// and no OnChange function runs.
var errUnchanged = errors.New("settings unchanged")

// settingsReply is the answer of the settings routes: the extra field of the
// route and the server settings as the browser sees them.
type settingsReply struct {
	Updated        *bool                 `json:"updated,omitempty"`
	RowsUpdated    *int                  `json:"rowsUpdated,omitempty"`
	ServerSettings settings.ServerPublic `json:"serverSettings"`
}

// update applies fn to the server settings; a patch that changes nothing writes nothing.
func (h *Handler) update(r *http.Request, fn func(*settings.Server) error) (changed bool, snapshot settings.Server, err error) {
	var invalid error // a bad patch, not a failure of the store
	snapshot, err = h.settings.UpdateServer(r.Context(), func(s *settings.Server) error {
		before := *s                                           // the other slices are replaced, never edited in place
		before.AllowedOrigins = slices.Clone(s.AllowedOrigins) // decoding reuses the backing array
		if invalid = fn(s); invalid != nil {
			return invalid
		}
		if reflect.DeepEqual(before, *s) {
			return errUnchanged
		}
		return nil
	})
	switch {
	case invalid != nil:
		return false, snapshot, invalid
	case errors.Is(err, errUnchanged):
		return false, h.settings.Server(), nil
	case err != nil:
		return false, snapshot, httpx.Internal("database error", err)
	}
	return true, snapshot, nil
}

// patchBody is a PATCH body by key; only the keys a route lists are read.
type patchBody map[string]json.RawMessage

// field ties a patchable key to the settings field it overwrites.
type field struct {
	key string
	dst any // pointer to the field; JSON null leaves a plain field alone and clears a NullString or Secret
}

// decode overwrites the fields whose keys are present: a mistyped value is a 400.
func (b patchBody) decode(fields []field) error {
	for _, f := range fields {
		if raw, ok := b[f.key]; ok && json.Unmarshal(raw, f.dst) != nil {
			return httpx.Errorf(http.StatusBadRequest, "Invalid value for %q", f.key)
		}
	}
	return nil
}

func (h *Handler) patchSettings(w http.ResponseWriter, r *http.Request) error {
	body, err := httpx.Decode[patchBody](w, r)
	if err != nil {
		return err
	}
	if string(body["allowIframe"]) == "false" && h.getenv("ALLOW_IFRAME") == "1" {
		return httpx.BadRequest("Cannot disable iframe when ALLOW_IFRAME is enabled in environment")
	}
	_, snapshot, err := h.update(r, func(s *settings.Server) error {
		return body.decode([]field{
			{"scannerParseSubtitle", &s.ScannerParseSubtitle}, {"scannerFindCovers", &s.ScannerFindCovers},
			{"scannerCoverProvider", &s.ScannerCoverProvider}, {"scannerPreferMatchedMetadata", &s.ScannerPreferMatchedData},
			{"scannerDisableWatcher", &s.ScannerDisableWatcher}, {"storeCoverWithItem", &s.StoreCoverWithItem},
			{"storeMetadataWithItem", &s.StoreMetadataWithItem}, {"allowIframe", &s.AllowIframe},
			{"allowedOrigins", &s.AllowedOrigins}, {"backupSchedule", &s.BackupSchedule}, {"backupsToKeep", &s.BackupsToKeep},
			{"maxBackupSize", &s.MaxBackupSize}, {"logLevel", &s.LogLevel}, {"homeBookshelfView", &s.HomeBookshelfView},
			{"bookshelfView", &s.BookshelfView}, {"dateFormat", &s.DateFormat}, {"timeFormat", &s.TimeFormat},
			{"language", &s.Language}, {"chromecastEnabled", &s.ChromecastEnabled}, {"sortingIgnorePrefix", &s.SortingIgnorePrefix},
		})
	})
	if err != nil {
		return err
	}
	return httpx.JSON(w, r, settingsReply{ServerSettings: snapshot.Public()})
}

// sortingPrefixes saves the prefixes and recomputes the ignore-prefix columns.
func (h *Handler) sortingPrefixes(w http.ResponseWriter, r *http.Request) error {
	body, err := httpx.Decode[struct {
		SortingPrefixes []string `json:"sortingPrefixes"`
	}](w, r)
	if err != nil || len(body.SortingPrefixes) == 0 {
		return httpx.BadRequest("Invalid request body")
	}
	var prefixes []string
	for _, prefix := range body.SortingPrefixes {
		if prefix = strings.ToLower(strings.TrimSpace(prefix)); prefix != "" && !slices.Contains(prefixes, prefix) {
			prefixes = append(prefixes, prefix)
		}
	}
	if len(prefixes) == 0 {
		return httpx.BadRequest("Invalid sortingPrefixes in request body")
	}
	_, snapshot, err := h.update(r, func(s *settings.Server) error { s.SortingPrefixes = prefixes; return nil })
	if err != nil {
		return err
	}
	rows := 0
	err = h.db.Tx(r.Context(), func(ctx context.Context) error {
		for _, table := range []struct{ name, title, ignore string }{
			{"books", "title", "titleIgnorePrefix"}, {"podcasts", "title", "titleIgnorePrefix"}, {"series", "name", "nameIgnorePrefix"},
		} {
			var titles []struct {
				ID     string  `db:"id"`
				Title  *string `db:"title"`
				Ignore *string `db:"ignored"`
			}
			if err := h.db.Select(ctx, &titles, "SELECT id, "+table.title+" AS title, "+table.ignore+" AS ignored FROM "+table.name); err != nil {
				return err
			}
			for _, row := range titles {
				want := text.TitleIgnorePrefix(db.Val(row.Title), prefixes)
				if want == db.Val(row.Ignore) {
					continue
				}
				if _, err := h.db.Exec(ctx, "UPDATE "+table.name+" SET "+table.ignore+" = ? WHERE id = ?", want, row.ID); err != nil {
					return err
				}
				rows++
			}
		}
		return nil
	})
	if err != nil {
		return httpx.Internal("database error", err)
	}
	return httpx.JSON(w, r, settingsReply{RowsUpdated: &rows, ServerSettings: snapshot.Public()})
}

// authSettingsView is GET /api/auth-settings: the admin view of the auth keys
// plus the fixed sample the web client shows next to the claim settings.
type authSettingsView struct {
	settings.AuthSettings
	SamplePermissions string `json:"authOpenIDSamplePermissions"`
}

func (h *Handler) authSettings(w http.ResponseWriter, r *http.Request) error {
	return httpx.JSON(w, r, authSettingsView{h.settings.Server().Auth(), samplePermissions})
}

var mobileURI = regexp.MustCompile(`(?i)^\w+://[\w.-]+(/[\w./-]*)*$`)

// applyAuth changes s by the keys of body. Invalid methods or redirect URIs
// leave their setting as it was (AuthController.updateAuthSettings).
func applyAuth(s *settings.Server, body patchBody) error {
	var methods, uris []string
	err := body.decode([]field{
		{"authLoginCustomMessage", &s.LoginCustomMessage}, {"authOpenIDIssuerURL", &s.OpenIDIssuerURL},
		{"authOpenIDAuthorizationURL", &s.OpenIDAuthorizeURL}, {"authOpenIDTokenURL", &s.OpenIDTokenURL},
		{"authOpenIDUserInfoURL", &s.OpenIDUserInfoURL}, {"authOpenIDJwksURL", &s.OpenIDJwksURL},
		{"authOpenIDLogoutURL", &s.OpenIDLogoutURL}, {"authOpenIDClientID", &s.OpenIDClientID},
		{"authOpenIDClientSecret", &s.OpenIDClientSecret}, {"authOpenIDTokenSigningAlgorithm", &s.OpenIDSigningAlg},
		{"authOpenIDButtonText", &s.OpenIDButtonText}, {"authOpenIDAutoLaunch", &s.OpenIDAutoLaunch},
		{"authOpenIDAutoRegister", &s.OpenIDAutoRegister}, {"authOpenIDMatchExistingBy", &s.OpenIDMatchExistingBy},
		{"authOpenIDGroupClaim", &s.OpenIDGroupClaim}, {"authOpenIDAdvancedPermsClaim", &s.OpenIDAdvancedPermsClaim},
		{"authActiveAuthMethods", &methods}, {"authOpenIDMobileRedirectURIs", &uris},
	})
	if err != nil {
		return err
	}
	if _, ok := body["authLoginCustomMessage"]; ok {
		s.LoginCustomMessage = settings.NullString(text.Sanitize(string(s.LoginCustomMessage)))
	}
	if raw, ok := body["authOpenIDSubfolderForRedirectURLs"]; ok { // "" stays "" here; null unsets
		var subfolder *string
		if json.Unmarshal(raw, &subfolder) != nil {
			return httpx.BadRequest(`Invalid value for "authOpenIDSubfolderForRedirectURLs"`)
		}
		s.OpenIDSubfolder = subfolder
	}
	methods = slices.DeleteFunc(methods, func(m string) bool { return m != "local" && m != "openid" })
	slices.Sort(methods)
	if methods = slices.Compact(methods); len(methods) > 0 {
		s.ActiveAuthMethods = methods
	}
	// "*" alone, or custom-scheme URIs the mobile app can open.
	if uris != nil && !(slices.Contains(uris, "*") && len(uris) > 1) && !slices.ContainsFunc(uris, func(u string) bool { return u != "*" && !mobileURI.MatchString(u) }) {
		s.OpenIDMobileRedirectURIs = uris
	}
	return nil
}

func (h *Handler) patchAuthSettings(w http.ResponseWriter, r *http.Request) error {
	body, err := httpx.Decode[patchBody](w, r)
	if err != nil {
		return err
	}
	changed, snapshot, err := h.update(r, func(s *settings.Server) error { return applyAuth(s, body) })
	if err != nil {
		return err
	}
	return httpx.JSON(w, r, settingsReply{Updated: &changed, ServerSettings: snapshot.Public()})
}

// samplePermissions is the fixed example shown next to the advanced
// permissions claim (AuthSettings.authOpenIDSamplePermissions).
//
//go:embed sample_permissions.json
var samplePermissions string
