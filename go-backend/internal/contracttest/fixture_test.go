package contracttest

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootServesSeededLibraryToListener(t *testing.T) {
	in := Boot(t)
	listener := in.Users["listener"]
	token := in.Login(t, listener.Username, listener.Password)

	resp, body := in.Do(t, http.MethodGet, "/api/libraries", token, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/libraries: status %d: %s", resp.StatusCode, body)
	}
	var out struct {
		Libraries []struct {
			ID string `json:"id"`
		} `json:"libraries"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("GET /api/libraries: decode: %v: %s", err, body)
	}
	want := in.IDs["library"]
	for _, lib := range out.Libraries {
		if lib.ID == want {
			return
		}
	}
	t.Fatalf("library %s not in GET /api/libraries: %s", want, body)
}

func TestBootRewritesEverySeedPath(t *testing.T) {
	in := Boot(t)
	fx := readJSON[fixtureMeta](t, filepath.Join(fixtureDir, fixtureFile))
	left, err := countSeedPaths(context.Background(), in.DB, fx.SeedRoots.Media, fx.SeedRoots.Metadata, fx.SeedRoots.Config)
	if err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("%d database cells still hold a seed path", left)
	}
	var folder string
	if err := in.DB.QueryRow(`SELECT path FROM libraryFolders LIMIT 1`).Scan(&folder); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(in.Dir, "media"); !strings.HasPrefix(folder, want) {
		t.Fatalf("libraryFolders.path = %q, want under %q", folder, want)
	}
}
