// Package config contains the small set of process settings used by the first
// experimental HTTP server slice.
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config is the normalized process configuration. CONFIG_PATH and
// METADATA_PATH follow the upstream directory defaults; this read-only slice
// never creates or mutates either directory.
type Config struct {
	Port           int
	Host           string
	ConfigPath     string
	MetadataPath   string
	RouterBasePath string
	ClientPath     string
	Source         string
	JWTSecret      string
	ReadOnly       bool
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
		JWTSecret: os.Getenv("JWT_SECRET_KEY"), ReadOnly: os.Getenv("ABS_GO_READ_ONLY") == "1",
	}, nil
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// normalizeBasePath matches Server.js: trim whitespace, add a leading slash,
// and remove one trailing slash.
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
