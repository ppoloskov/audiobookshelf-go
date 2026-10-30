package config

import (
	"os"
	"testing"
)

func TestLoadRouterBasePathAndPort(t *testing.T) {
	tests := []struct {
		name, base, port string
		setBase          bool
		wantBase         string
		wantPort         int
		wantErr          bool
	}{
		{name: "upstream default", port: "", wantBase: "/audiobookshelf", wantPort: 3333},
		{name: "explicit empty", base: "", setBase: true, port: "", wantBase: "", wantPort: 3333},
		{name: "nested path", base: " /media/abs ", setBase: true, port: "4444", wantBase: "/media/abs", wantPort: 4444},
		{name: "remove one trailing slash", base: "media/abs///", setBase: true, port: "1234", wantBase: "/media/abs//", wantPort: 1234},
		{name: "multiple root slashes", base: "///", setBase: true, port: "3333", wantBase: "//", wantPort: 3333},
		{name: "invalid port", base: "", setBase: true, port: "nope", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PORT", tc.port)
			if tc.setBase {
				t.Setenv("ROUTER_BASE_PATH", tc.base)
			} else {
				oldValue, wasSet := os.LookupEnv("ROUTER_BASE_PATH")
				if err := os.Unsetenv("ROUTER_BASE_PATH"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if wasSet {
						_ = os.Setenv("ROUTER_BASE_PATH", oldValue)
					} else {
						_ = os.Unsetenv("ROUTER_BASE_PATH")
					}
				})
			}
			cfg, err := LoadArgs(nil)
			if tc.wantErr {
				if err == nil {
					t.Fatal("Load() error = nil, want invalid-port error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.RouterBasePath != tc.wantBase || cfg.Port != tc.wantPort {
				t.Fatalf("Load() base=%q port=%d, want base=%q port=%d", cfg.RouterBasePath, cfg.Port, tc.wantBase, tc.wantPort)
			}
		})
	}
}

func TestNormalizeBasePath(t *testing.T) {
	for input, want := range map[string]string{
		"": "", "/": "", "   ": "", "media///": "/media//",
		" /nested/path/// ": "/nested/path//", "already/": "/already",
	} {
		if got := normalizeBasePath(input); got != want {
			t.Errorf("normalizeBasePath(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestLoadArgsOverridesEnvironment(t *testing.T) {
	t.Setenv("CONFIG_PATH", "/env/config")
	t.Setenv("METADATA_PATH", "/env/metadata")
	t.Setenv("PORT", "3333")
	t.Setenv("HOST", "127.0.0.1")
	t.Setenv("SOURCE", "env-source")
	cfg, err := LoadArgs([]string{"-c", "/cli/config", "--metadata=/cli/metadata", "-p", "4555", "--host", "0.0.0.0", "-s", "cli-source"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConfigPath != "/cli/config" || cfg.MetadataPath != "/cli/metadata" || cfg.Port != 4555 || cfg.Host != "0.0.0.0" || cfg.Source != "cli-source" {
		t.Fatalf("unexpected CLI config: %#v", cfg)
	}
}
