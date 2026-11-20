package oracle

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func jsonResponse(status int, body string) Response {
	return Response{
		Status: status,
		Header: http.Header{"Content-Type": {"application/json; charset=utf-8"}},
		Body:   []byte(body),
	}
}

func makeTestJWT(t *testing.T, payload map[string]any, sig string) string {
	t.Helper()
	hBytes, err := json.Marshal(map[string]any{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		t.Fatalf("marshal jwt header: %v", err)
	}
	pBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal jwt payload: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(hBytes) + "." +
		base64.RawURLEncoding.EncodeToString(pBytes) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(sig))
}

func TestDiffHTTPMissingAndExtraFields(t *testing.T) {
	d := NewDiffer("", "", nil)
	tsResp := jsonResponse(200, `{"id":"lib-1","name":"Books","extraTS":true}`)
	goResp := jsonResponse(200, `{"id":"lib-1","extraGo":1}`)

	findings := d.DiffHTTP("get-lib", tsResp, goResp)
	if len(findings) != 3 {
		t.Fatalf("expected 3 findings (missing name, extraTS, extraGo), got %d:\n%s", len(findings), FormatFindings(findings))
	}
	paths := map[string]bool{}
	for _, f := range findings {
		paths[f.Path] = true
	}
	for _, want := range []string{"body.name", "body.extraTS", "body.extraGo"} {
		if !paths[want] {
			t.Fatalf("expected finding at %s, got %v", want, paths)
		}
	}
}

func TestDiffHTTPNullVsEmptyAndSlice(t *testing.T) {
	d := NewDiffer("", "", nil)
	tsResp := jsonResponse(200, `{"subtitle":null,"tags":[]}`)
	goResp := jsonResponse(200, `{"subtitle":"","tags":null}`)

	findings := d.DiffHTTP("get-item", tsResp, goResp)
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings, got %d:\n%s", len(findings), FormatFindings(findings))
	}
	if findings[0].Path != "body.subtitle" || findings[1].Path != "body.tags" {
		t.Fatalf("unexpected finding paths: %+v", findings)
	}
}

func TestDiffHTTPNumberVsString(t *testing.T) {
	d := NewDiffer("", "", nil)
	tsResp := jsonResponse(200, `{"duration":120,"flag":false}`)
	goResp := jsonResponse(200, `{"duration":"120","flag":0}`)

	findings := d.DiffHTTP("get-track", tsResp, goResp)
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings (number vs string, bool vs number), got %d:\n%s", len(findings), FormatFindings(findings))
	}
}

func TestDiffHTTPBijectiveUUIDCorrespondence(t *testing.T) {
	d := NewDiffer("", "", nil)
	tsA := "11111111-1111-4111-8111-111111111111"
	goA := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	goB := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"

	if f := d.DiffHTTP("create-item", jsonResponse(200, `{"id":"`+tsA+`"}`), jsonResponse(200, `{"id":"`+goA+`"}`)); len(f) != 0 {
		t.Fatalf("unexpected findings on initial UUID bind: %s", FormatFindings(f))
	}
	if f := d.DiffHTTP("get-item", jsonResponse(200, `{"itemId":"`+tsA+`"}`), jsonResponse(200, `{"itemId":"`+goA+`"}`)); len(f) != 0 {
		t.Fatalf("unexpected findings on consistent UUID reuse: %s", FormatFindings(f))
	}

	findings := d.DiffHTTP("get-item-bad", jsonResponse(200, `{"itemId":"`+tsA+`"}`), jsonResponse(200, `{"itemId":"`+goB+`"}`))
	if len(findings) != 1 || !strings.Contains(findings[0].Detail, "UUID") {
		t.Fatalf("expected bijective UUID correspondence failure, got:\n%s", FormatFindings(findings))
	}
}

func TestDiffHTTPJWTClaimMismatchAndSignatureNormalization(t *testing.T) {
	d := NewDiffer("", "", nil)
	tsUserID := "11111111-1111-4111-8111-111111111111"
	goUserID := "22222222-2222-4222-8222-222222222222"

	tsJWT := makeTestJWT(t, map[string]any{"userId": tsUserID, "type": "admin", "iat": 1700000000, "exp": 1700003600}, "ts-secret-sig")
	goJWTMatch := makeTestJWT(t, map[string]any{"userId": goUserID, "type": "admin", "iat": 1700000050, "exp": 1700003650}, "go-different-sig")

	if f := d.DiffHTTP("login-ok", jsonResponse(200, `{"token":"`+tsJWT+`"}`), jsonResponse(200, `{"token":"`+goJWTMatch+`"}`)); len(f) != 0 {
		t.Fatalf("expected matching JWT claims with different signatures/iat to pass, got:\n%s", FormatFindings(f))
	}

	goJWTMismatch := makeTestJWT(t, map[string]any{"userId": goUserID, "type": "user", "iat": 1700000050, "exp": 1700003650}, "go-different-sig")
	findings := d.DiffHTTP("login-role-bug", jsonResponse(200, `{"token":"`+tsJWT+`"}`), jsonResponse(200, `{"token":"`+goJWTMismatch+`"}`))
	if len(findings) != 1 || findings[0].Path != "body.token.payload.type" {
		t.Fatalf("expected JWT type claim mismatch at body.token.payload.type, got:\n%s", FormatFindings(findings))
	}
}

func TestDiffSQLiteStorageClassAndRowDiff(t *testing.T) {
	dir := t.TempDir()
	tsDB := filepath.Join(dir, "ts.sqlite")
	goDB := filepath.Join(dir, "go.sqlite")

	// Untyped/BLOB column has NONE affinity in SQLite so 1 stays integer and '1' stays text.
	createSQL := `CREATE TABLE users (id TEXT PRIMARY KEY, isActive BLOB, permissions TEXT);`
	execSQL(t, tsDB, createSQL+`INSERT INTO users (id, isActive, permissions) VALUES ('11111111-1111-4111-8111-111111111111', 1, '{"download":true,"upload":false}');`)
	execSQL(t, goDB, createSQL+`INSERT INTO users (id, isActive, permissions) VALUES ('22222222-2222-4222-8222-222222222222', '1', '{"upload":false,"download":true}');`)

	d := NewDiffer("", "", nil)
	d.Corr.BindID("11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222")

	findings, err := d.DiffSQLite("check-users", tsDB, goDB)
	if err != nil {
		t.Fatalf("DiffSQLite failed: %v", err)
	}
	if len(findings) != 1 || !strings.HasSuffix(findings[0].Path, ".isActive.typeof") {
		t.Fatalf("expected exactly 1 typeof mismatch on isActive (and JSON permissions key-order ignored), got:\n%s", FormatFindings(findings))
	}

	before, err := SnapshotDB(tsDB)
	if err != nil {
		t.Fatalf("snapshot before: %v", err)
	}
	if unchanged := DiffDBUnchanged("rejected-req", before, before); len(unchanged) != 0 {
		t.Fatalf("expected no findings for identical snapshots, got:\n%s", FormatFindings(unchanged))
	}
	execSQL(t, tsDB, `UPDATE users SET isActive = 0;`)
	after, err := SnapshotDB(tsDB)
	if err != nil {
		t.Fatalf("snapshot after: %v", err)
	}
	if mutated := DiffDBUnchanged("rejected-req", before, after); len(mutated) != 1 {
		t.Fatalf("expected 1 mutation finding from DiffDBUnchanged, got:\n%s", FormatFindings(mutated))
	}
}

func execSQL(t *testing.T, dbPath, query string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite %s: %v", dbPath, err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close sqlite %s: %v", dbPath, err)
		}
	}()
	if _, err := db.Exec(query); err != nil {
		t.Fatalf("exec sqlite %s: %v", dbPath, err)
	}
}

func TestDiffSocketsAudienceLeak(t *testing.T) {
	d := NewDiffer("", "", nil)
	tsEvents := []SocketEvent{
		{Role: "admin", Name: "user_added", Args: []json.RawMessage{json.RawMessage(`{"username":"alice"}`)}, At: time.Now()},
		{Role: "listener", Name: "user_added", Args: []json.RawMessage{json.RawMessage(`{"username":"alice"}`)}, At: time.Now()},
	}
	goEvents := []SocketEvent{
		{Role: "admin", Name: "user_added", Args: []json.RawMessage{json.RawMessage(`{"username":"alice"}`)}, At: time.Now()},
	}

	findings := d.DiffSockets("create-user", tsEvents, goEvents)
	if len(findings) != 1 || findings[0].Path != "listener:user_added" {
		t.Fatalf("expected audience leak finding for listener:user_added, got:\n%s", FormatFindings(findings))
	}
}

func TestDiffFSMetadataJSON(t *testing.T) {
	root := t.TempDir()
	tsDir := filepath.Join(root, "ts")
	goDir := filepath.Join(root, "go")
	tsItemID := "11111111-1111-4111-8111-111111111111"
	goItemID := "22222222-2222-4222-8222-222222222222"

	writeTestFile(t, filepath.Join(tsDir, "metadata", "items", tsItemID, "metadata.json"), `{"title":"Dune","author":"Frank Herbert"}`)
	writeTestFile(t, filepath.Join(tsDir, "metadata", "logs", "app.log"), "ts-volatile-log")
	writeTestFile(t, filepath.Join(goDir, "metadata", "items", goItemID, "metadata.json"), `{"author":"Frank Herbert","title":"Dune"}`)

	d := NewDiffer(tsDir, goDir, nil)
	d.Corr.BindID(tsItemID, goItemID)

	findings, err := d.DiffFS("fs-match", tsDir, goDir)
	if err != nil {
		t.Fatalf("DiffFS failed: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected 0 FS findings with normalized UUID path & reordered JSON, got:\n%s", FormatFindings(findings))
	}

	writeTestFile(t, filepath.Join(goDir, "metadata", "items", goItemID, "metadata.json"), `{"author":"Frank Herbert","title":"Dune Messiah"}`)
	findings, err = d.DiffFS("fs-mutated", tsDir, goDir)
	if err != nil {
		t.Fatalf("DiffFS failed: %v", err)
	}
	if len(findings) != 1 || !strings.HasSuffix(findings[0].Path, "metadata.json.title") {
		t.Fatalf("expected 1 metadata.json title mismatch, got:\n%s", FormatFindings(findings))
	}
}

func writeTestFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

func TestDivergenceLedgerGoExpectGuard(t *testing.T) {
	ledger := &DivergenceLedger{
		Rules: []DivergenceRule{
			{
				ID:           "DIV-001",
				Scope:        "http",
				Surface:      "http",
				StepPattern:  "auth/*",
				FieldPattern: "body.serverVersion",
				TSExpect:     "2.37.2",
				GoExpect:     "2.37.2-go",
				Reason:       "Go reports -go suffix",
				ClientImpact: "none",
			},
		},
	}
	d := NewDiffer("", "", ledger)

	expectedF := Finding{Step: "auth/status", Surface: "http", Path: "body.serverVersion", TSValue: "2.37.2", GoValue: "2.37.2-go"}
	unexp, ack := d.Filter([]Finding{expectedF})
	if len(unexp) != 0 || len(ack) != 1 {
		t.Fatalf("expected finding to be acknowledged when go_expect matches, got unexp=%d ack=%d", len(unexp), len(ack))
	}

	unexpectedF := Finding{Step: "auth/status", Surface: "http", Path: "body.serverVersion", TSValue: "2.37.2", GoValue: "0.0.0-broken"}
	unexp, ack = d.Filter([]Finding{unexpectedF})
	if len(unexp) != 1 || len(ack) != 0 {
		t.Fatalf("expected finding NOT to be masked when go_expect mismatches, got unexp=%d ack=%d", len(unexp), len(ack))
	}
}

func TestDiffHTTPIdenticalTimeStringsAndLastEpisodeCheck(t *testing.T) {
	d := NewDiffer("", "", nil)
	tsResp := jsonResponse(200, `{"completionTime":"12:00 AM","start_time":"0.050113","lastEpisodeCheck":1700000000000}`)
	goResp := jsonResponse(200, `{"completionTime":"12:00 AM","start_time":"0.050113","lastEpisodeCheck":1700000005000}`)

	findings := d.DiffHTTP("check-times", tsResp, goResp)
	if len(findings) != 0 {
		t.Fatalf("expected 0 findings for identical *Time strings and epoch-ms lastEpisodeCheck, got:\n%s", FormatFindings(findings))
	}

	mismatch := d.DiffHTTP("bad-time",
		jsonResponse(200, `{"completionTime":"12:00 AM"}`),
		jsonResponse(200, `{"completionTime":"1:00 AM"}`),
	)
	if len(mismatch) != 1 || mismatch[0].Path != "body.completionTime" {
		t.Fatalf("expected invalid timestamp finding on mismatched completionTime, got:\n%s", FormatFindings(mismatch))
	}
}

func buildTestZip(t *testing.T, method uint16, mtime time.Time, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		hdr := &zip.FileHeader{
			Name:     name,
			Method:   method,
			Modified: mtime,
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatalf("create zip header %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("write zip entry %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func TestDiffHTTPZipEntriesAndBackupFileSize(t *testing.T) {
	d := NewDiffer("", "", nil)
	tsZip := buildTestZip(t, zip.Deflate, time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), map[string]string{
		"absdatabase.sqlite": "sqlite-bytes",
		"details":            "backup-details",
	})
	goZip := buildTestZip(t, zip.Store, time.Date(2026, 2, 2, 12, 0, 0, 0, time.UTC), map[string]string{
		"details":            "backup-details",
		"absdatabase.sqlite": "sqlite-bytes",
	})

	zipResp := func(b []byte) Response {
		return Response{
			Status: 200,
			Header: http.Header{"Content-Type": {"application/zip"}},
			Body:   b,
		}
	}
	if findings := d.DiffHTTP("download-zip-match", zipResp(tsZip), zipResp(goZip)); len(findings) != 0 {
		t.Fatalf("expected matching zip entries with different mtime/compression to pass, got:\n%s", FormatFindings(findings))
	}

	missingZip := buildTestZip(t, zip.Deflate, time.Now(), map[string]string{
		"absdatabase.sqlite": "sqlite-bytes",
	})
	findings := d.DiffHTTP("download-zip-missing", zipResp(tsZip), zipResp(missingZip))
	if len(findings) != 1 || findings[0].Detail != "zip entry list mismatch" {
		t.Fatalf("expected zip entry list mismatch, got:\n%s", FormatFindings(findings))
	}

	emptyEntryZip := buildTestZip(t, zip.Deflate, time.Now(), map[string]string{
		"absdatabase.sqlite": "sqlite-bytes",
		"details":            "",
	})
	findings = d.DiffHTTP("download-zip-empty-entry", zipResp(tsZip), zipResp(emptyEntryZip))
	if len(findings) != 1 || findings[0].Detail != "zip archive validation failed" {
		t.Fatalf("expected zip archive validation failure for empty entry, got:\n%s", FormatFindings(findings))
	}

	if f := d.DiffHTTP("list-backups-ok",
		jsonResponse(200, `{"backups":[{"fileSize":1000}]}`),
		jsonResponse(200, `{"backups":[{"fileSize":650}]}`),
	); len(f) != 0 {
		t.Fatalf("expected backup fileSize within 50%% to pass, got:\n%s", FormatFindings(f))
	}

	if f := d.DiffHTTP("list-backups-too-far",
		jsonResponse(200, `{"backups":[{"fileSize":1000}]}`),
		jsonResponse(200, `{"backups":[{"fileSize":400}]}`),
	); len(f) != 1 || f[0].Path != "body.backups[0].fileSize" {
		t.Fatalf("expected backup fileSize >50%% apart to fail, got:\n%s", FormatFindings(f))
	}
}

func TestDiffStubURLAndSMTPPortNormalization(t *testing.T) {
	d := NewDiffer("", "", nil)
	d.Corr.RegisterStubs(
		"http://127.0.0.1:41000/ts",
		"http://127.0.0.1:41000/go",
		"127.0.0.1:41001",
		"127.0.0.1:41002",
	)

	tsBody := `{"webhook":{"url":"http://127.0.0.1:41000/ts/hook"},"smtp":{"addr":"127.0.0.1:41001","port":41001}}`
	goBody := `{"webhook":{"url":"http://127.0.0.1:41000/go/hook"},"smtp":{"addr":"127.0.0.1:41002","port":41002}}`
	if f := d.DiffHTTP("stub-settings", jsonResponse(200, tsBody), jsonResponse(200, goBody)); len(f) != 0 {
		t.Fatalf("expected stub URL, SMTP addr, and SMTP port to normalize identically, got:\n%s", FormatFindings(f))
	}

	badPort := `{"webhook":{"url":"http://127.0.0.1:41000/go/hook"},"smtp":{"addr":"127.0.0.1:41002","port":25}}`
	findings := d.DiffHTTP("stub-bad-port", jsonResponse(200, tsBody), jsonResponse(200, badPort))
	if len(findings) != 1 || findings[0].Path != "body.smtp.port" {
		t.Fatalf("expected mismatch on non-stub port 25, got:\n%s", FormatFindings(findings))
	}

	dir := t.TempDir()
	tsDB := filepath.Join(dir, "ts.sqlite")
	goDB := filepath.Join(dir, "go.sqlite")
	createSQL := `CREATE TABLE emailSettings (id TEXT PRIMARY KEY, host TEXT, port INTEGER);`
	execSQL(t, tsDB, createSQL+`INSERT INTO emailSettings (id, host, port) VALUES ('email', '127.0.0.1:41001', 41001);`)
	execSQL(t, goDB, createSQL+`INSERT INTO emailSettings (id, host, port) VALUES ('email', '127.0.0.1:41002', 41002);`)
	dbFindings, err := d.DiffSQLite("stub-db-port", tsDB, goDB)
	if err != nil {
		t.Fatalf("DiffSQLite failed: %v", err)
	}
	if len(dbFindings) != 0 {
		t.Fatalf("expected stub SMTP addr/port in SQLite to pass, got:\n%s", FormatFindings(dbFindings))
	}
}

func TestDiffUnorderedArraysAndDailyLogs(t *testing.T) {
	d := NewDiffer("", "", nil)
	tsU1 := "11111111-1111-4111-8111-111111111111"
	tsU2 := "22222222-2222-4222-8222-222222222222"
	goU1 := "ffffffff-ffff-4fff-8fff-ffffffffffff"
	goU2 := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	d.Corr.BindID(tsU1, goU1)
	d.Corr.BindID(tsU2, goU2)

	tsBody := `{"usersOnline":[{"id":"` + tsU1 + `","name":"a"},{"id":"` + tsU2 + `","name":"b"}],"booksAddedWithCovers":["` + tsU2 + `","` + tsU1 + `"],"currentDailyLogs":[{"msg":"boot"},{"msg":"ready"}]}`
	goBody := `{"usersOnline":[{"id":"` + goU2 + `","name":"b"},{"id":"` + goU1 + `","name":"a"}],"booksAddedWithCovers":["` + goU1 + `","` + goU2 + `"],"currentDailyLogs":[{"msg":"ready"}]}`
	if f := d.DiffHTTP("unordered-arrays", jsonResponse(200, tsBody), jsonResponse(200, goBody)); len(f) != 0 {
		t.Fatalf("expected unordered set arrays and non-empty currentDailyLogs to pass, got:\n%s", FormatFindings(f))
	}

	emptyLogs := d.DiffHTTP("empty-daily-logs",
		jsonResponse(200, `{"currentDailyLogs":[{"msg":"boot"}]}`),
		jsonResponse(200, `{"currentDailyLogs":[]}`),
	)
	if len(emptyLogs) != 1 || emptyLogs[0].Path != "body.currentDailyLogs" {
		t.Fatalf("expected failure when currentDailyLogs is empty, got:\n%s", FormatFindings(emptyLogs))
	}
}

func TestDiffDynamicAndFixtureInodes(t *testing.T) {
	root := t.TempDir()
	tsMedia := filepath.Join(root, "ts", "media")
	goMedia := filepath.Join(root, "go", "media")
	writeTestFile(t, filepath.Join(tsMedia, "book.mp3"), "audio")
	writeTestFile(t, filepath.Join(goMedia, "book.mp3"), "audio")

	d := NewDiffer("", "", nil)
	d.Corr.RegisterInodes(tsMedia, goMedia, "", "")

	var tsKnownIno string
	d.Corr.mu.RLock()
	for k := range d.Corr.tsInodes {
		tsKnownIno = k
	}
	d.Corr.mu.RUnlock()
	if tsKnownIno == "" {
		t.Fatal("expected registered fixture inode")
	}

	if f := d.DiffHTTP("dynamic-ino-ok",
		jsonResponse(200, `{"ino":"99999901"}`),
		jsonResponse(200, `{"ino":"99999902"}`),
	); len(f) != 0 {
		t.Fatalf("expected unregistered numeric inodes to pass, got:\n%s", FormatFindings(f))
	}

	if f := d.DiffHTTP("dynamic-ino-nonnumeric",
		jsonResponse(200, `{"ino":"99999901"}`),
		jsonResponse(200, `{"ino":"not-an-ino"}`),
	); len(f) != 1 {
		t.Fatalf("expected non-numeric inode to fail, got:\n%s", FormatFindings(f))
	}

	if f := d.DiffHTTP("fixture-ino-mismatch",
		jsonResponse(200, `{"ino":"`+tsKnownIno+`"}`),
		jsonResponse(200, `{"ino":"99999902"}`),
	); len(f) != 1 {
		t.Fatalf("expected registered fixture inode vs unregistered inode to fail, got:\n%s", FormatFindings(f))
	}
}

func TestDiffFeedEpisodesAlignmentAndURLBinding(t *testing.T) {
	dir := t.TempDir()
	tsDB := filepath.Join(dir, "ts.sqlite")
	goDB := filepath.Join(dir, "go.sqlite")

	createSQL := `CREATE TABLE feedEpisodes (id TEXT PRIMARY KEY, title TEXT, filePath TEXT, season TEXT, episode TEXT, enclosureURL TEXT);`
	tsEp1 := "11111111-1111-4111-8111-111111111111"
	tsEp2 := "22222222-2222-4222-8222-222222222222"
	goEp1 := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	goEp2 := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

	execSQL(t, tsDB, createSQL+
		`INSERT INTO feedEpisodes VALUES ('`+tsEp1+`','Ep 1','/media/ep1.mp3','1','1','/feed/my-cast/item/`+tsEp1+`/media.mp3');`+
		`INSERT INTO feedEpisodes VALUES ('`+tsEp2+`','Ep 2','/media/ep2.mp3','1','2','/feed/my-cast/item/`+tsEp2+`/media.mp3');`)
	execSQL(t, goDB, createSQL+
		`INSERT INTO feedEpisodes VALUES ('`+goEp2+`','Ep 2','/media/ep2.mp3','1','2','/feed/my-cast/item/`+goEp2+`/media.mp3');`+
		`INSERT INTO feedEpisodes VALUES ('`+goEp1+`','Ep 1','/media/ep1.mp3','1','1','/feed/my-cast/item/`+goEp1+`/media.mp3');`)

	d := NewDiffer("", "", nil)
	findings, err := d.DiffSQLite("feed-episodes", tsDB, goDB)
	if err != nil {
		t.Fatalf("DiffSQLite failed: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected feedEpisodes to align by metadata and bind enclosureURL UUIDs, got:\n%s", FormatFindings(findings))
	}
	if got, ok := d.Corr.MapForGo(tsEp1); !ok || got != goEp1 {
		t.Fatalf("expected %s -> %s bound, got (%q, %v)", tsEp1, goEp1, got, ok)
	}
}

func TestPodcastSocketLedgerRule(t *testing.T) {
	ledger := DefaultDivergences()
	d := NewDiffer("", "", ledger)
	unexp, ack := d.Filter([]Finding{{
		Step:    "podcast-create-episode",
		Surface: "socket",
		Path:    "listener:item_added",
		TSValue: "1 events",
		GoValue: "0 events",
		Detail:  "socket event count mismatch",
	}})
	if len(unexp) != 0 || len(ack) != 1 {
		t.Fatalf("expected podcast socket item filter rule to acknowledge 0 events, got unexp=%d ack=%d", len(unexp), len(ack))
	}
}

func TestDiffHTTPXMLWhitespaceNormalization(t *testing.T) {
	d := NewDiffer("", "", nil)
	xmlResp := func(ct, body string) Response {
		return Response{
			Status: 200,
			Header: http.Header{"Content-Type": {ct}},
			Body:   []byte(body),
		}
	}

	tsXML := `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title><![CDATA[My  Feed]]></title><link>https://example.test/feed</link></channel></rss>`
	goXML := "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<rss version=\"2.0\">\n  <channel>\n    <title><![CDATA[My  Feed]]></title>\n    <link>https://example.test/feed</link>\n  </channel>\n</rss>\n"

	if f := d.DiffHTTP("get-feed-xml", xmlResp("application/xml; charset=utf-8", tsXML), xmlResp("application/xml; charset=utf-8", goXML)); len(f) != 0 {
		t.Fatalf("expected semantically identical XML with declaration/inter-tag whitespace to pass, got:\n%s", FormatFindings(f))
	}

	if f := d.DiffHTTP("get-feed-xml-prefix", xmlResp("", tsXML), xmlResp("", goXML)); len(f) != 0 {
		t.Fatalf("expected <?xml-prefixed bodies without Content-Type to pass, got:\n%s", FormatFindings(f))
	}

	diffTextXML := strings.Replace(goXML, "https://example.test/feed", "https://example.test/other", 1)
	if f := d.DiffHTTP("get-feed-xml-text-diff", xmlResp("text/xml", tsXML), xmlResp("text/xml", diffTextXML)); len(f) != 1 {
		t.Fatalf("expected XML text content difference to fail, got:\n%s", FormatFindings(f))
	}

	diffCDATAXML := strings.Replace(goXML, "My  Feed", "My Feed", 1)
	if f := d.DiffHTTP("get-feed-xml-cdata-diff", xmlResp("application/xml", tsXML), xmlResp("application/xml", diffCDATAXML)); len(f) != 1 {
		t.Fatalf("expected XML CDATA whitespace difference to fail, got:\n%s", FormatFindings(f))
	}

	diffAttrXML := strings.Replace(goXML, `version="2.0"`, `version="1.0"`, 1)
	if f := d.DiffHTTP("get-feed-xml-attr-diff", xmlResp("application/xml", tsXML), xmlResp("application/xml", diffAttrXML)); len(f) != 1 {
		t.Fatalf("expected XML attribute difference to fail, got:\n%s", FormatFindings(f))
	}
}

func TestDiffStubsSMTPEmailMIME(t *testing.T) {
	d := NewDiffer("", "", nil)

	tsPlain := "Content-Type: text/plain; charset=utf-8\r\n" +
		"From: abs@example.test\r\n" +
		"To: user@example.test\r\n" +
		"Subject: Test email from Audiobookshelf\r\n" +
		"Message-ID: <ts-msg-1@example.test>\r\n" +
		"Date: Fri, 20 Nov 2026 10:00:00 +0000\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Transfer-Encoding: 7bit\r\n" +
		"\r\n" +
		"Success!\r\n"

	goPlain := "From: <abs@example.test>\r\n" +
		"To: <user@example.test>\r\n" +
		"Subject: Test email from Audiobookshelf\r\n" +
		"Date: Fri, 20 Nov 2026 10:00:01 +0000\r\n" +
		"Message-ID: <go-msg-2@example.test>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" +
		"Success!\r\n"

	if f := d.DiffStubs("emails-send-test",
		[]StubCall{{Kind: "smtp", Method: "DATA", Path: "user@example.test", Body: tsPlain}},
		[]StubCall{{Kind: "smtp", Method: "DATA", Path: "user@example.test", Body: goPlain}},
	); len(f) != 0 {
		t.Fatalf("expected semantically identical plain SMTP emails to pass, got:\n%s", FormatFindings(f))
	}

	ebookBytes := []byte("EPUB-BINARY-PAYLOAD-0123456789")
	ebookB64 := base64.StdEncoding.EncodeToString(ebookBytes)

	tsMulti := "Content-Type: multipart/mixed; boundary=\"----=_Part_TS_123\"\r\n" +
		"From: abs@example.test\r\n" +
		"To: kindle@example.test\r\n" +
		"Subject: Here is your Ebook!\r\n" +
		"Message-ID: <ts-eb-1@example.test>\r\n" +
		"Date: Fri, 20 Nov 2026 10:00:00 +0000\r\n" +
		"MIME-Version: 1.0\r\n" +
		"\r\n" +
		"------=_Part_TS_123\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"Content-Transfer-Encoding: 7bit\r\n" +
		"\r\n" +
		"<div dir=\"auto\"></div>\r\n" +
		"------=_Part_TS_123\r\n" +
		"Content-Type: application/epub+zip; name=\"book.epub\"\r\n" +
		"Content-Disposition: attachment; filename=\"book.epub\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"\r\n" +
		ebookB64[:12] + "\r\n" + ebookB64[12:] + "\r\n" +
		"------=_Part_TS_123--\r\n"

	goMulti := "From: <abs@example.test>\r\n" +
		"To: <kindle@example.test>\r\n" +
		"Subject: Here is your Ebook!\r\n" +
		"Date: Fri, 20 Nov 2026 10:00:01 +0000\r\n" +
		"Message-ID: <go-eb-2@example.test>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=goBoundary999\r\n" +
		"\r\n" +
		"--goBoundary999\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n" +
		"\r\n" +
		"<div dir=\"auto\"></div>\r\n" +
		"--goBoundary999\r\n" +
		"Content-Disposition: attachment; filename=\"book.epub\"\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"Content-Type: application/octet-stream; name=\"book.epub\"\r\n" +
		"\r\n" +
		ebookB64 + "\r\n" +
		"--goBoundary999--\r\n"

	if f := d.DiffStubs("emails-send-ebook-to-device",
		[]StubCall{{Kind: "smtp", Method: "DATA", Path: "kindle@example.test", Body: tsMulti}},
		[]StubCall{{Kind: "smtp", Method: "DATA", Path: "kindle@example.test", Body: goMulti}},
	); len(f) != 0 {
		t.Fatalf("expected semantically identical multipart SMTP emails to pass, got:\n%s", FormatFindings(f))
	}

	// Verify attachment byte mismatch still fails.
	badMulti := strings.Replace(goMulti, ebookB64, base64.StdEncoding.EncodeToString([]byte("WRONG-PAYLOAD")), 1)
	if f := d.DiffStubs("emails-bad-attachment",
		[]StubCall{{Kind: "smtp", Method: "DATA", Path: "kindle@example.test", Body: tsMulti}},
		[]StubCall{{Kind: "smtp", Method: "DATA", Path: "kindle@example.test", Body: badMulti}},
	); len(f) != 1 || !strings.HasSuffix(f[0].Path, ".parts[1].body") {
		t.Fatalf("expected attachment byte mismatch to fail, got:\n%s", FormatFindings(f))
	}

	// Verify filename and header mismatches still fail.
	badNameMulti := strings.ReplaceAll(goMulti, "book.epub", "other.epub")
	if f := d.DiffStubs("emails-bad-filename",
		[]StubCall{{Kind: "smtp", Method: "DATA", Path: "kindle@example.test", Body: tsMulti}},
		[]StubCall{{Kind: "smtp", Method: "DATA", Path: "kindle@example.test", Body: badNameMulti}},
	); len(f) != 1 || !strings.HasSuffix(f[0].Path, ".parts[1].filename") {
		t.Fatalf("expected attachment filename mismatch to fail, got:\n%s", FormatFindings(f))
	}

	badSubjPlain := strings.Replace(goPlain, "Test email from Audiobookshelf", "Wrong subject", 1)
	if f := d.DiffStubs("emails-bad-subject",
		[]StubCall{{Kind: "smtp", Method: "DATA", Path: "user@example.test", Body: tsPlain}},
		[]StubCall{{Kind: "smtp", Method: "DATA", Path: "user@example.test", Body: badSubjPlain}},
	); len(f) != 1 || !strings.HasSuffix(f[0].Path, ".subject") {
		t.Fatalf("expected email subject mismatch to fail, got:\n%s", FormatFindings(f))
	}

	// Verify fallback to raw string diff when email headers cannot be parsed.
	if f := d.DiffStubs("emails-unparseable-fallback",
		[]StubCall{{Kind: "smtp", Method: "DATA", Path: "user@example.test", Body: "not-valid-rfc5322-a"}},
		[]StubCall{{Kind: "smtp", Method: "DATA", Path: "user@example.test", Body: "not-valid-rfc5322-b"}},
	); len(f) != 1 || f[0].Detail != "stub body mismatch" {
		t.Fatalf("expected unparseable SMTP body to fall back to raw string diff, got:\n%s", FormatFindings(f))
	}
}

func TestDiffStubsHTTPQueryParamCanonicalization(t *testing.T) {
	d := NewDiffer("", "", nil)
	tsCall := StubCall{Kind: "http", Method: "GET", Path: "/qm/search?mediaType=book&query=The+Beginning&author=Terry+Fixture"}
	goCall := StubCall{Kind: "http", Method: "GET", Path: "/qm/search?author=Terry+Fixture&mediaType=book&query=The+Beginning"}

	if f := d.DiffStubs("quick-match-saga1", []StubCall{tsCall}, []StubCall{goCall}); len(f) != 0 {
		t.Fatalf("expected reordered query parameters to pass, got:\n%s", FormatFindings(f))
	}

	badGoCall := StubCall{Kind: "http", Method: "GET", Path: "/qm/search?author=Other+Author&mediaType=book&query=The+Beginning"}
	if f := d.DiffStubs("quick-match-mismatch", []StubCall{tsCall}, []StubCall{badGoCall}); len(f) != 1 || f[0].Path != "calls[0].path" {
		t.Fatalf("expected differing query parameter value to fail, got:\n%s", FormatFindings(f))
	}
}

func TestDefaultDivergencesHTTPHeaderRules(t *testing.T) {
	ledger := DefaultDivergences()
	d := NewDiffer("", "", ledger)

	cases := []struct {
		name    string
		finding Finding
		wantAck bool
	}{
		{
			name:    "accept-ranges bytes on Go vs empty on TS acknowledged",
			finding: Finding{Step: "get-cover-raw", Surface: "http", Path: "header.Accept-Ranges", TSValue: "", GoValue: "bytes"},
			wantAck: true,
		},
		{
			name:    "accept-ranges regression empty on Go not masked",
			finding: Finding{Step: "get-cover-raw", Surface: "http", Path: "header.Accept-Ranges", TSValue: "bytes", GoValue: ""},
			wantAck: false,
		},
		{
			name:    "backups-download application/zip acknowledged",
			finding: Finding{Step: "backups-download", Surface: "http", Path: "header.Content-Type", TSValue: "application/octet-stream", GoValue: "application/zip"},
			wantAck: true,
		},
		{
			name:    "backups-download wrong content-type not masked",
			finding: Finding{Step: "backups-download", Surface: "http", Path: "header.Content-Type", TSValue: "application/octet-stream", GoValue: "text/plain"},
			wantAck: false,
		},
		{
			name:    "backups-patch-path-unchanged text/plain acknowledged",
			finding: Finding{Step: "backups-patch-path-unchanged", Surface: "http", Path: "header.Content-Type", TSValue: "text/html; charset=utf-8", GoValue: "text/plain; charset=utf-8"},
			wantAck: true,
		},
		{
			name:    "backups-patch-path-unchanged wrong content-type not masked",
			finding: Finding{Step: "backups-patch-path-unchanged", Surface: "http", Path: "header.Content-Type", TSValue: "text/html; charset=utf-8", GoValue: "application/json"},
			wantAck: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unexp, ack := d.Filter([]Finding{tc.finding})
			if tc.wantAck && (len(unexp) != 0 || len(ack) != 1) {
				t.Fatalf("expected finding to be acknowledged, got unexp=%d ack=%d", len(unexp), len(ack))
			}
			if !tc.wantAck && (len(unexp) != 1 || len(ack) != 0) {
				t.Fatalf("expected finding NOT to be acknowledged, got unexp=%d ack=%d", len(unexp), len(ack))
			}
		})
	}
}
