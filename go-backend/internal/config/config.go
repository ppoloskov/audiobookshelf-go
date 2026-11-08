// Package config contains the process settings: ports, directories and the
// few environment switches the server reads at startup.
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port           int
	Host           string
	ConfigPath     string
	MetadataPath   string
	RouterBasePath string
	ClientPath     string
	Source         string
	JWTSecret      string
	Auth           Auth
	// MaxFailedEpisodeChecks is MAX_FAILED_EPISODE_CHECKS (Server.js:119):
	// failed episode checks before a podcast's auto-download is switched off;
	// 0 never switches it off.
	MaxFailedEpisodeChecks int
}

// Auth is the token lifetime and login rate-limit environment. The zero value
// means "not loaded"; DefaultAuth is what an empty environment gives.
type Auth struct {
	AccessTTL, RefreshTTL, RefreshGrace time.Duration
	RateMax                             int // 0 disables the limit
	RateWindow                          time.Duration
	RateMessage                         string
}

// DefaultAuth is the upstream default of every Auth setting.
func DefaultAuth() Auth {
	return Auth{AccessTTL: time.Hour, RefreshTTL: 30 * 24 * time.Hour, RefreshGrace: 10 * time.Minute, RateMax: 40, RateWindow: 10 * time.Minute, RateMessage: "Too many authentication requests"}
}

// authFromEnv reads ACCESS_TOKEN_EXPIRY, REFRESH_TOKEN_EXPIRY,
// REFRESH_TOKEN_GRACE_PERIOD (seconds) and RATE_LIMIT_AUTH_MAX, _WINDOW (ms),
// _MESSAGE; an unset, invalid or (but for RATE_LIMIT_AUTH_MAX) zero value
// keeps the default.
func authFromEnv() Auth {
	a := DefaultAuth()
	a.AccessTTL = envSeconds("ACCESS_TOKEN_EXPIRY", a.AccessTTL)
	a.RefreshTTL = envSeconds("REFRESH_TOKEN_EXPIRY", a.RefreshTTL)
	a.RefreshGrace = envSeconds("REFRESH_TOKEN_GRACE_PERIOD", a.RefreshGrace)
	a.RateMax = envInt("RATE_LIMIT_AUTH_MAX", a.RateMax, true)
	a.RateWindow = time.Duration(envInt("RATE_LIMIT_AUTH_WINDOW", int(a.RateWindow/time.Millisecond), false)) * time.Millisecond
	if value := os.Getenv("RATE_LIMIT_AUTH_MESSAGE"); value != "" {
		a.RateMessage = value
	}
	return a
}

func envSeconds(name string, fallback time.Duration) time.Duration {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value == 0 {
		return fallback
	}
	return time.Duration(value) * time.Second
}

func envInt(name string, fallback int, allowZero bool) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value < 0 || (value == 0 && !allowZero) {
		return fallback
	}
	return value
}

// LoadArgs applies the command line options supported by index.js over the
// corresponding environment defaults. The short and long option forms are
// accepted for compatibility with the production launcher.
func LoadArgs(args []string) (Config, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return Config{}, fmt.Errorf("get working directory: %w", err)
	}
	portDefault := 3333
	if raw := strings.TrimSpace(os.Getenv("PORT")); raw != "" {
		portDefault, err = strconv.Atoi(raw)
		if err != nil || portDefault < 1 || portDefault > 65535 {
			return Config{}, fmt.Errorf("invalid PORT %q", raw)
		}
	}
	configPathDefault := envOr("CONFIG_PATH", filepath.Join(cwd, "config"))
	metadataPathDefault := envOr("METADATA_PATH", filepath.Join(cwd, "metadata"))
	hostDefault := strings.TrimSpace(os.Getenv("HOST"))
	sourceDefault := envOr("SOURCE", "debian")
	var configPath, metadataPath, host, source string
	var port int
	fs := flag.NewFlagSet("audiobookshelf-go", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&configPath, "config", configPathDefault, "configuration directory")
	fs.StringVar(&configPath, "c", configPathDefault, "configuration directory")
	fs.StringVar(&metadataPath, "metadata", metadataPathDefault, "metadata directory")
	fs.StringVar(&metadataPath, "m", metadataPathDefault, "metadata directory")
	fs.IntVar(&port, "port", portDefault, "HTTP port")
	fs.IntVar(&port, "p", portDefault, "HTTP port")
	fs.StringVar(&host, "host", hostDefault, "HTTP bind host")
	fs.StringVar(&host, "h", hostDefault, "HTTP bind host")
	fs.StringVar(&source, "source", sourceDefault, "runtime source label")
	fs.StringVar(&source, "s", sourceDefault, "runtime source label")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() != 0 {
		return Config{}, fmt.Errorf("unexpected positional arguments: %s", strings.Join(fs.Args(), " "))
	}
	if port < 1 || port > 65535 {
		return Config{}, fmt.Errorf("invalid port %d", port)
	}
	configPath, err = filepath.Abs(filepath.Clean(configPath))
	if err != nil {
		return Config{}, fmt.Errorf("resolve CONFIG_PATH: %w", err)
	}
	metadataPath, err = filepath.Abs(filepath.Clean(metadataPath))
	if err != nil {
		return Config{}, fmt.Errorf("resolve METADATA_PATH: %w", err)
	}
	basePath, ok := os.LookupEnv("ROUTER_BASE_PATH")
	if !ok {
		basePath = "/audiobookshelf"
	}
	base := normalizeBasePath(basePath)
	clientPath, clientPathSet := os.LookupEnv("ABS_GO_CLIENT_PATH")
	if !clientPathSet || strings.TrimSpace(clientPath) == "" {
		clientPath = filepath.Join(cwd, "client", "dist")
		if _, statErr := os.Stat(clientPath); statErr != nil {
			clientPath = filepath.Join(cwd, "..", "client", "dist")
		}
	}
	clientPath, err = filepath.Abs(filepath.Clean(clientPath))
	if err != nil {
		return Config{}, fmt.Errorf("resolve ABS_GO_CLIENT_PATH: %w", err)
	}
	return Config{
		Port: port, Host: strings.TrimSpace(host),
		ConfigPath: configPath, MetadataPath: metadataPath,
		RouterBasePath: base, ClientPath: clientPath, Source: source,
		JWTSecret: os.Getenv("JWT_SECRET_KEY"), Auth: authFromEnv(),
		MaxFailedEpisodeChecks: envInt("MAX_FAILED_EPISODE_CHECKS", 24, true),
	}, nil
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func normalizeBasePath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "/" {
		return ""
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	if strings.HasSuffix(value, "/") {
		value = strings.TrimSuffix(value, "/")
	}
	return value
}
