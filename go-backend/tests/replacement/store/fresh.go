package store

import (
	"net/http"
	"reflect"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

func freshRoot(t *testing.T, _ *harness.Env) {
	e := harness.Start(t, harness.Options{Fresh: true})
	schema := e.Rows(t, "SELECT type,name,tbl_name,sql FROM sqlite_master WHERE type IN ('table','trigger') AND name NOT LIKE 'sqlite_%' ORDER BY type,name")
	assertSchemaNames(t, e)
	if e.Count(t, "SELECT COUNT(*) FROM users") != 0 {
		t.Fatal("fresh database already contains users")
	}
	r := e.Post(t, "", "/init", map[string]any{"newRoot": map[string]string{"username": "store-fresh-root", "password": "store-fresh-password"}})
	if r.Status != http.StatusOK {
		t.Fatalf("init status %d", r.Status)
	}
	rows := e.Rows(t, "SELECT id,username,type,isActive FROM users")
	if len(rows) != 1 || rows[0]["username"] != "store-fresh-root" || rows[0]["type"] != "root" || rows[0]["isActive"] != int64(1) || rows[0]["id"] == "" {
		t.Fatal("fresh root identity invalid")
	}
	id, ok := rows[0]["id"].(string)
	if !ok || id == "" {
		t.Fatal("root id is not a nonempty string")
	}
	e.Stop(t)
	reopened := harness.Start(t, harness.Options{Install: e.Install, Upstream: true})
	if got := reopened.Rows(t, "SELECT type,name,tbl_name,sql FROM sqlite_master WHERE type IN ('table','trigger') AND name NOT LIKE 'sqlite_%' ORDER BY type,name"); !reflect.DeepEqual(got, schema) {
		t.Fatal("fresh schema changed on reopen")
	}
	if got := reopened.Rows(t, "SELECT id,username,type,isActive FROM users"); !reflect.DeepEqual(got, rows) {
		t.Fatal("fresh root identity changed on reopen")
	}
	_, user := reopened.Login(t, "store-fresh-root", "store-fresh-password")
	if user["id"] != id || user["type"] != "root" {
		t.Fatal("reopened login has wrong identity")
	}
}
