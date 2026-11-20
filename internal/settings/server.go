package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
)

// Defaults of a fresh server (ServerSettings constructor and the upstream
// v2.37.2 database init). Version and build number are those of the pinned
// upstream release, which the database is compatible with.
const (
	defaultVersion     = "2.37.2"
	defaultBuildNumber = 1
	defaultLogLevel    = 2 // upstream LogLevel.INFO
)

// ServerPublic is the part of the server settings every logged-in browser may
// see: ServerSettings.toJSONForBrowser, plus TimeZone.
type ServerPublic struct {
	ID                       string         `json:"id"`
	ScannerFindCovers        bool           `json:"scannerFindCovers"`
	ScannerCoverProvider     string         `json:"scannerCoverProvider"`
	ScannerParseSubtitle     bool           `json:"scannerParseSubtitle"`
	ScannerPreferMatchedData bool           `json:"scannerPreferMatchedMetadata"`
	ScannerDisableWatcher    bool           `json:"scannerDisableWatcher"`
	StoreCoverWithItem       bool           `json:"storeCoverWithItem"`
	StoreMetadataWithItem    bool           `json:"storeMetadataWithItem"`
	MetadataFileFormat       string         `json:"metadataFileFormat"`
	RateLimitLoginRequests   int            `json:"rateLimitLoginRequests"`
	RateLimitLoginWindow     int            `json:"rateLimitLoginWindow"` // milliseconds
	AllowIframe              bool           `json:"allowIframe"`
	BackupPath               string         `json:"backupPath"`
	BackupSchedule           BackupSchedule `json:"backupSchedule"`
	BackupsToKeep            int            `json:"backupsToKeep"`
	MaxBackupSize            float64        `json:"maxBackupSize"` // gigabytes, 0 = unlimited
	LoggerDailyLogsToKeep    int            `json:"loggerDailyLogsToKeep"`
	LoggerScannerLogsToKeep  int            `json:"loggerScannerLogsToKeep"`
	HomeBookshelfView        int            `json:"homeBookshelfView"` // 0 standard, 1 detail
	BookshelfView            int            `json:"bookshelfView"`
	PodcastEpisodeSchedule   string         `json:"podcastEpisodeSchedule"`
	SortingIgnorePrefix      bool           `json:"sortingIgnorePrefix"`
	SortingPrefixes          []string       `json:"sortingPrefixes"`
	ChromecastEnabled        bool           `json:"chromecastEnabled"`
	DateFormat               string         `json:"dateFormat"`
	TimeFormat               string         `json:"timeFormat"`
	Language                 string         `json:"language"`
	AllowedOrigins           []string       `json:"allowedOrigins"`
	LogLevel                 int            `json:"logLevel"`
	Version                  string         `json:"version"`
	BuildNumber              int            `json:"buildNumber"`
	TimeZone                 string         `json:"timeZone,omitzero"` // only in the browser view, never stored
	AuthPublic
}

// AuthPublic is the authentication settings a browser may see.
type AuthPublic struct {
	LoginCustomMessage    NullString `json:"authLoginCustomMessage"`
	ActiveAuthMethods     []string   `json:"authActiveAuthMethods"` // only "local" and "openid"
	OpenIDIssuerURL       NullString `json:"authOpenIDIssuerURL"`
	OpenIDAuthorizeURL    NullString `json:"authOpenIDAuthorizationURL"`
	OpenIDTokenURL        NullString `json:"authOpenIDTokenURL"`
	OpenIDUserInfoURL     NullString `json:"authOpenIDUserInfoURL"`
	OpenIDJwksURL         NullString `json:"authOpenIDJwksURL"`
	OpenIDLogoutURL       NullString `json:"authOpenIDLogoutURL"`
	OpenIDSigningAlg      string     `json:"authOpenIDTokenSigningAlgorithm"`
	OpenIDButtonText      string     `json:"authOpenIDButtonText"`
	OpenIDAutoLaunch      bool       `json:"authOpenIDAutoLaunch"`
	OpenIDAutoRegister    bool       `json:"authOpenIDAutoRegister"`
	OpenIDMatchExistingBy NullString `json:"authOpenIDMatchExistingBy"`
	OpenIDSubfolder       *string    `json:"authOpenIDSubfolderForRedirectURLs,omitempty"` // nil: never set
}

// AuthPrivate is the authentication settings only admins may see.
type AuthPrivate struct {
	OpenIDClientID           Secret   `json:"authOpenIDClientID"`
	OpenIDClientSecret       Secret   `json:"authOpenIDClientSecret"`
	OpenIDMobileRedirectURIs []string `json:"authOpenIDMobileRedirectURIs"`
	OpenIDGroupClaim         string   `json:"authOpenIDGroupClaim"`
	OpenIDAdvancedPermsClaim string   `json:"authOpenIDAdvancedPermsClaim"`
}

// AuthSettings is the document of GET /api/auth-settings: every auth key of
// the server settings (ServerSettings.authenticationSettings, without the
// sample permissions, which the handler adds).
type AuthSettings struct {
	AuthPublic
	AuthPrivate
}

// Server is the server-settings document (ServerSettings.toJSON). It holds
// secrets: use Public or Auth for anything that leaves the process. Values
// returned by a Store are the caller's own copy.
type Server struct {
	ServerPublic
	AuthPrivate
	TokenSecret Secret `json:"tokenSecret"` // JWT key when JWT_SECRET_KEY is not set
}

// DefaultServer is the document of a fresh install, without the environment
// overrides (see WithEnv) and without a token secret. backupsDir is the
// default backup folder.
func DefaultServer(backupsDir string) Server {
	return Server{
		ServerPublic: ServerPublic{
			ID: ServerKey, ScannerCoverProvider: "google", MetadataFileFormat: "json",
			RateLimitLoginRequests: 10, RateLimitLoginWindow: 10 * 60 * 1000,
			BackupPath: backupsDir, BackupsToKeep: 2, MaxBackupSize: 1,
			LoggerDailyLogsToKeep: 7, LoggerScannerLogsToKeep: 2,
			HomeBookshelfView: 1, BookshelfView: 1, PodcastEpisodeSchedule: "0 * * * *",
			SortingPrefixes: []string{"the", "a"}, DateFormat: "MM/dd/yyyy", TimeFormat: "HH:mm",
			Language: "en-us", AllowedOrigins: []string{}, LogLevel: defaultLogLevel,
			Version: defaultVersion, BuildNumber: defaultBuildNumber,
			AuthPublic: AuthPublic{ActiveAuthMethods: []string{"local"}, OpenIDSigningAlg: "RS256", OpenIDButtonText: "Login with OpenId"},
		},
		AuthPrivate: AuthPrivate{OpenIDMobileRedirectURIs: []string{"audiobookshelf://oauth"}},
	}
}

func (s *Store) defaultServer() Server { return DefaultServer(s.backupsDir) }

// WithEnv applies the environment overrides: BACKUP_PATH replaces backupPath
// and ALLOW_IFRAME=1 forces allowIframe on. The Store applies them to the
// snapshot it serves, but never writes them to the row.
func (s Server) WithEnv(getenv func(string) string) Server {
	if path := getenv("BACKUP_PATH"); path != "" {
		s.BackupPath = path
	}
	if getenv("ALLOW_IFRAME") == "1" {
		s.AllowIframe = true
	}
	return s
}

func (s *Store) withEnv(server Server) *Server {
	server = server.WithEnv(s.getenv)
	return &server
}

// clone returns a copy that shares no slice with s.
func (s *Server) clone() Server {
	c := *s
	c.SortingPrefixes = slices.Clone(s.SortingPrefixes)
	c.AllowedOrigins = slices.Clone(s.AllowedOrigins)
	c.ActiveAuthMethods = slices.Clone(s.ActiveAuthMethods)
	c.OpenIDMobileRedirectURIs = slices.Clone(s.OpenIDMobileRedirectURIs)
	return c
}

// normalize applies what upstream's constructor does after reading a row. The
// Store also applies it before every save, so a stored document always has the
// shape upstream would have written (active methods only local/openid, and
// openid only while its settings are complete).
func (s *Server) normalize() {
	s.ID = ServerKey
	s.TimeZone = ""               // a view-only key, never stored
	s.MetadataFileFormat = "json" // as of upstream v2.4.5 only json is supported
	if s.SortingPrefixes == nil {
		s.SortingPrefixes = []string{"the", "a"}
	}
	if s.AllowedOrigins == nil {
		s.AllowedOrigins = []string{}
	}
	if s.OpenIDMobileRedirectURIs == nil {
		s.OpenIDMobileRedirectURIs = []string{"audiobookshelf://oauth"}
	}
	if s.OpenIDSigningAlg == "" {
		s.OpenIDSigningAlg = "RS256" // `|| 'RS256'`
	}
	if s.OpenIDButtonText == "" {
		s.OpenIDButtonText = "Login with OpenId"
	}
	methods := s.ActiveAuthMethods[:0:0]
	for _, method := range s.ActiveAuthMethods {
		if method == "local" || (method == "openid" && s.openIDValid()) {
			methods = append(methods, method)
		}
	}
	if len(methods) == 0 {
		methods = []string{"local"}
	}
	s.ActiveAuthMethods = methods
}

// openIDValid is ServerSettings.isOpenIDAuthSettingsValid.
func (s *Server) openIDValid() bool {
	return s.OpenIDIssuerURL != "" && s.OpenIDAuthorizeURL != "" && s.OpenIDTokenURL != "" &&
		s.OpenIDUserInfoURL != "" && s.OpenIDJwksURL != "" && s.OpenIDClientID != "" &&
		s.OpenIDClientSecret != "" && s.OpenIDSigningAlg != ""
}

// Public is the document browsers receive (toJSONForBrowser): no token
// secret and none of the private auth keys. The host time zone is filled in.
func (s Server) Public() ServerPublic {
	view := s.clone().ServerPublic
	view.TimeZone = hostTimeZone()
	return view
}

// Auth is the document of GET /api/auth-settings (admins only).
func (s Server) Auth() AuthSettings {
	c := s.clone()
	return AuthSettings{AuthPublic: c.AuthPublic, AuthPrivate: c.AuthPrivate}
}

// String prints no setting at all, so %v and %+v cannot log a secret.
func (Server) String() string { return "settings.Server{redacted}" }

// String prints no setting at all, so %v and %+v cannot log a secret.
func (AuthSettings) String() string { return "settings.AuthSettings{redacted}" }

// Secret is a string that is null in JSON when empty and never prints.
type Secret string

// MarshalJSON writes the empty secret as null, like upstream's null default.
func (s Secret) MarshalJSON() ([]byte, error) { return marshalNullable(string(s)) }

// UnmarshalJSON reads null as the empty secret.
func (s *Secret) UnmarshalJSON(data []byte) error { return unmarshalNullable((*string)(s), data) }

// String hides the value from fmt.
func (Secret) String() string { return "redacted" }

// GoString hides the value from %#v.
func (Secret) GoString() string { return "settings.Secret(redacted)" }

// NullString is a string that is null in JSON when empty (upstream stores
// `value || null` for these keys).
type NullString string

// MarshalJSON writes the empty string as null.
func (s NullString) MarshalJSON() ([]byte, error) { return marshalNullable(string(s)) }

// UnmarshalJSON reads null as the empty string.
func (s *NullString) UnmarshalJSON(data []byte) error { return unmarshalNullable((*string)(s), data) }

func marshalNullable(s string) ([]byte, error) {
	if s == "" {
		return []byte("null"), nil
	}
	return json.Marshal(s)
}

func unmarshalNullable(dst *string, data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		*dst = ""
		return nil
	}
	return json.Unmarshal(data, dst)
}

// BackupSchedule is a cron expression; the empty schedule (automatic backups
// off) is stored as false, which is what upstream writes.
type BackupSchedule string

// MarshalJSON writes the empty schedule as false.
func (b BackupSchedule) MarshalJSON() ([]byte, error) {
	if b == "" {
		return []byte("false"), nil
	}
	return json.Marshal(string(b))
}

// UnmarshalJSON reads false and null as no schedule.
func (b *BackupSchedule) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("false")) || bytes.Equal(data, []byte("null")) {
		*b = ""
		return nil
	}
	var cron string
	if err := json.Unmarshal(data, &cron); err != nil {
		return fmt.Errorf("backupSchedule: %w", err)
	}
	*b = BackupSchedule(cron)
	return nil
}
