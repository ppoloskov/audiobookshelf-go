package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestOutputIsCurrent fails when contracts/client-usage.json is not what the
// generator produces from the repository as it is now.
func TestOutputIsCurrent(t *testing.T) {
	root, err := repositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	res, err := generate(root)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join(root, outputPath))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, res.JSON) {
		t.Fatalf("%s is stale: run `cd go-backend && go run ./tools/clientusage`", outputPath)
	}
}

func TestNormalize(t *testing.T) {
	// Inner text of the literal, without its quotes.
	cases := map[string]string{
		"/api/libraries/${this.id}/items?limit=${n}&page=0": "/api/libraries/:param/items",
		"${this.$config.routerBasePath}/api/items/x":        ":param/api/items/x",
		"/api/items/${ids[k]}?expanded=1":                   "/api/items/:param",
		"/api/tasks?include=queue":                          "/api/tasks",
		"/api/a/${f(x ? 1 : 2)}/b":                          "/api/a/:param/b",
	}
	for raw, want := range cases {
		if got := normalize(raw); got != want {
			t.Errorf("normalize(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestMatchScore(t *testing.T) {
	cases := []struct {
		client, upstream string
		ok               bool
	}{
		{"/api/libraries/:param/items", "/api/libraries/:id/items", true},
		{"/api/me/progress/:param", "/api/me/progress/:id/:episodeId?", true},
		{"/api/items/:param/ebook", "/api/items/:id/ebook/:fileid?", true},
		{"/api/:param", "/api/libraries", false},
		{"/api/items/:param/cover", "/api/items/:id/cover/extra", false},
		{"/feed/x/cover.jpg", "/feed/:slug/cover*", true},
		{"/api/search/books", "/api/search/:x", true},
	}
	for _, c := range cases {
		if _, ok := matchScore(c.client, c.upstream); ok != c.ok {
			t.Errorf("matchScore(%q, %q) ok = %v, want %v", c.client, c.upstream, ok, c.ok)
		}
	}
}

// TestScanClientFixture checks each client pattern on a small file: an
// axios chain, a template with a base path, a variable argument, a socket
// emit and listeners (the Socket.IO manager's listener is not an app event).
func TestScanClientFixture(t *testing.T) {
	repo := t.TempDir()
	dir := filepath.Join(repo, "client", "components")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "<script>\n" +
		"export default {\n" +
		"  methods: {\n" +
		"    async load() {\n" +
		"      await this.$axios\n" +
		"        .$patch(`/api/me/progress/${this.id}`, body)\n" +
		"      await this.$axios.$get('/api/tasks?include=queue')\n" +
		"      return this.$axios.$post(`/api/items/${id}/play/${ep}`, payload)\n" +
		"    },\n" +
		"    url() { return `${this.$config.routerBasePath}/api/items/${this.id}/cover?ts=1` },\n" +
		"    dyn() { return this.$axios.$get(this.url) },\n" +
		"    join() {\n" +
		"      this.$root.socket.emit('cancel_scan', id)\n" +
		"      this.socket.on('item_added', f)\n" +
		"      this.socket.io.on('reconnect', f)\n" +
		"    }\n" +
		"  }\n" +
		"}\n" +
		"</script>\n"
	if err := os.WriteFile(filepath.Join(dir, "x.vue"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	scan, err := scanClient(repo)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range scan.calls {
		got = append(got, c.Method+" "+c.Path)
	}
	want := []string{
		"PATCH /api/me/progress/:param",
		"GET /api/tasks",
		"POST /api/items/:param/play/:param",
		" /api/items/:param/cover",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("calls = %q, want %q", got, want)
	}
	if len(scan.unresolved) != 1 {
		t.Errorf("unresolved = %q, want the one variable argument", scan.unresolved)
	}
	if len(scan.emits) != 1 || scan.emits[0].Event != "cancel_scan" || scan.emits[0].Caller != "client/components/x.vue:13" {
		t.Errorf("emits = %+v", scan.emits)
	}
	if len(scan.listens) != 1 || scan.listens[0].Event != "item_added" {
		t.Errorf("listens = %+v, want only item_added", scan.listens)
	}
}
