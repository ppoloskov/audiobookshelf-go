package emailapi

import (
	"audiobookshelf-compat/internal/store"
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/config"
	_ "modernc.org/sqlite"
)

func TestEmailSettingsUseUpstreamDefaultsAndPersistFirstUpdate(t *testing.T) {
	db, err := sql.Open("sqlite", "file:email-settings-defaults?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE settings(key TEXT PRIMARY KEY,value JSON,createdAt DATETIME NOT NULL,updatedAt DATETIME NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	h := New(db, config.Config{}, nil)
	admin := store.User{Type: "admin"}
	get := httptest.NewRecorder()
	h.Handle(get, httptest.NewRequest(http.MethodGet, "/api/emails/settings", nil), admin)
	if get.Code != http.StatusOK {
		t.Fatalf("GET status %d: %s", get.Code, get.Body)
	}
	var response struct {
		Settings map[string]any `json:"settings"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Settings["port"] != float64(465) || response.Settings["secure"] != true || response.Settings["ereaderDevices"] == nil {
		t.Fatalf("upstream defaults not returned: %#v", response.Settings)
	}
	patch := httptest.NewRecorder()
	h.Handle(patch, httptest.NewRequest(http.MethodPatch, "/api/emails/settings", strings.NewReader(`{"host":"smtp.example.invalid","port":"587","secure":"false"}`)), admin)
	if patch.Code != http.StatusOK {
		t.Fatalf("PATCH status %d: %s", patch.Code, patch.Body)
	}
	if err := json.Unmarshal(patch.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Settings["secure"] != true || response.Settings["host"] != "smtp.example.invalid" || response.Settings["port"] != float64(587) {
		t.Fatalf("JavaScript update coercion mismatch: %#v", response.Settings)
	}
	var persisted string
	if err := db.QueryRow(`SELECT value FROM settings WHERE key='email-settings'`).Scan(&persisted); err != nil {
		t.Fatalf("first setting update was not persisted: %v", err)
	}
	var saved map[string]any
	if err := json.Unmarshal([]byte(persisted), &saved); err != nil || saved["secure"] != true || saved["host"] != "smtp.example.invalid" || saved["port"] != float64(587) {
		t.Fatalf("persisted settings %#v, decode error %v", saved, err)
	}
}

func TestEreaderDeviceUpdateNormalizesOnceAndRetainsOptionalUsersShape(t *testing.T) {
	db, err := sql.Open("sqlite", "file:email-devices-normalization?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE settings(key TEXT PRIMARY KEY,value JSON,createdAt DATETIME NOT NULL,updatedAt DATETIME NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	var events []string
	h := New(db, config.Config{}, func(_ string, event string, _ any) { events = append(events, event) })
	admin := store.User{Type: "admin"}
	body := `{"ereaderDevices":[{"name":"Invalid option","email":"invalid@example.test","availabilityOption":"invalid","users":["other"]},{"name":"Empty specific users","email":"empty@example.test","availabilityOption":"specificUsers","users":[]},{"name":"Guest with empty users","email":"guest@example.test","availabilityOption":"guestOrUp","users":[]},{"name":"Missing users","email":"missing@example.test"},{"name":"Null users","email":"null@example.test","availabilityOption":"guestOrUp","users":null},{"name":"Specific users string","email":"string@example.test","availabilityOption":"specificUsers","users":"user-id"},{"name":"Guest object users","email":"object@example.test","availabilityOption":"guestOrUp","users":{"length":1}}]}`
	post := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.Handle(w, httptest.NewRequest(http.MethodPost, "/api/emails/ereader-devices", strings.NewReader(body)), admin)
		return w
	}
	first := post()
	if first.Code != http.StatusOK {
		t.Fatalf("first device update status %d: %s", first.Code, first.Body)
	}
	var response struct {
		Devices []map[string]any `json:"ereaderDevices"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Devices) != 7 {
		t.Fatalf("returned %d devices, want 7: %#v", len(response.Devices), response.Devices)
	}
	for _, index := range []int{0, 1} {
		if response.Devices[index]["availabilityOption"] != "adminOrUp" || len(anyArray(response.Devices[index]["users"])) != 0 {
			t.Fatalf("device %d normalization = %#v", index, response.Devices[index])
		}
	}
	if got := response.Devices[2]["users"]; !reflect.DeepEqual(got, []any{}) {
		t.Fatalf("explicit empty users changed: %#v", got)
	}
	if _, exists := response.Devices[3]["users"]; exists {
		t.Fatalf("missing users field was materialized: %#v", response.Devices[3])
	}
	if users, exists := response.Devices[4]["users"]; !exists || users != nil {
		t.Fatalf("null users changed: value=%#v present=%v", users, exists)
	}
	if response.Devices[5]["availabilityOption"] != "specificUsers" || response.Devices[5]["users"] != "user-id" {
		t.Fatalf("truthy string users shape changed: %#v", response.Devices[5])
	}
	if response.Devices[6]["availabilityOption"] != "guestOrUp" || !reflect.DeepEqual(response.Devices[6]["users"], []any{}) {
		t.Fatalf("truthy object users length was not normalized: %#v", response.Devices[6])
	}
	if len(events) != 1 || events[0] != "ereader-devices-updated" {
		t.Fatalf("first update events = %#v", events)
	}
	const sentinel = "2000-01-01 00:00:00.000 +00:00"
	if _, err := db.Exec(`UPDATE settings SET updatedAt=? WHERE key='email-settings'`, sentinel); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := db.QueryRow(`SELECT updatedAt FROM settings WHERE key='email-settings'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	repeat := post()
	if repeat.Code != http.StatusOK {
		t.Fatalf("repeat device update status %d: %s", repeat.Code, repeat.Body)
	}
	var after string
	if err := db.QueryRow(`SELECT updatedAt FROM settings WHERE key='email-settings'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("identical device update changed updatedAt from %q to %q", before, after)
	}
	if len(events) != 1 {
		t.Fatalf("identical device update emitted another event: %#v", events)
	}
	changed := strings.Replace(body, "invalid@example.test", "changed@example.test", 1)
	w := httptest.NewRecorder()
	h.Handle(w, httptest.NewRequest(http.MethodPost, "/api/emails/ereader-devices", strings.NewReader(changed)), admin)
	if w.Code != http.StatusOK {
		t.Fatalf("changed device update status %d: %s", w.Code, w.Body)
	}
	var afterChange string
	if err := db.QueryRow(`SELECT updatedAt FROM settings WHERE key='email-settings'`).Scan(&afterChange); err != nil {
		t.Fatal(err)
	}
	if afterChange == sentinel {
		t.Fatalf("changed device update did not persist: updatedAt %q", afterChange)
	}
	if len(events) != 2 {
		t.Fatalf("changed device update events = %#v, want one additional event", events)
	}
}

func TestCanAccessContentTagMatrix(t *testing.T) {
	// Upstream server/models/User.js:658-666: allowlist requires some overlap;
	// denylist allows untagged items and otherwise requires every tag absent.
	selections := [][]string{nil, {"x"}, {"x", "y"}}
	items := [][]string{nil, {"x"}, {"x", "y"}, {"z"}}
	for _, all := range []bool{false, true} {
		for _, deny := range []bool{false, true} {
			for _, selected := range selections {
				for _, tags := range items {
					name := fmt.Sprintf("all=%v/deny=%v/selected=%v/tags=%v", all, deny, selected, tags)
					t.Run(name, func(t *testing.T) {
						permissions, _ := json.Marshal(map[string]any{"accessExplicitContent": true, "accessAllTags": all, "selectedTagsNotAccessible": deny, "itemTagsSelected": selected})
						rawTags, _ := json.Marshal(tags)
						got := canAccessContent(store.User{Permissions: permissions}, 0, rawTags)
						want := all
						if !all {
							if deny {
								want = true
								for _, tag := range tags {
									if slices.Contains(selected, tag) {
										want = false
										break
									}
								}
							} else {
								for _, tag := range tags {
									if slices.Contains(selected, tag) {
										want = true
										break
									}
								}
							}
						}
						if got != want {
							t.Fatalf("got %v, want %v", got, want)
						}
					})
				}
			}
		}
	}
}

func TestCanAccessContentAllowlistPartialOverlap(t *testing.T) {
	permissions, _ := json.Marshal(map[string]any{"accessExplicitContent": true, "itemTagsSelected": []string{"x"}})
	tags, _ := json.Marshal([]string{"x", "y"})
	if !canAccessContent(store.User{Permissions: permissions}, 0, tags) {
		t.Fatal("allowlist must accept an item with at least one selected tag")
	}
}

func TestSendMailAgainstLocalSMTPServer(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	received := make(chan string, 1)
	errs := make(chan error, 1)
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			errs <- e
			return
		}
		defer conn.Close()
		_, _ = fmt.Fprint(conn, "220 localhost ESMTP\r\n")
		reader := bufio.NewReader(conn)
		data := false
		var msg strings.Builder
		for {
			line, e := reader.ReadString('\n')
			if e != nil {
				errs <- e
				return
			}
			trim := strings.TrimSpace(line)
			if data {
				if trim == "." {
					data = false
					_, _ = fmt.Fprint(conn, "250 queued\r\n")
					received <- msg.String()
					continue
				}
				msg.WriteString(line)
				continue
			}
			switch {
			case strings.HasPrefix(trim, "EHLO"), strings.HasPrefix(trim, "HELO"):
				_, _ = fmt.Fprint(conn, "250 localhost\r\n")
			case strings.HasPrefix(trim, "MAIL FROM"), strings.HasPrefix(trim, "RCPT TO"):
				_, _ = fmt.Fprint(conn, "250 ok\r\n")
			case trim == "DATA":
				data = true
				_, _ = fmt.Fprint(conn, "354 end with dot\r\n")
			case trim == "QUIT":
				_, _ = fmt.Fprint(conn, "221 bye\r\n")
				return
			default:
				_, _ = fmt.Fprint(conn, "250 ok\r\n")
			}
		}
	}()
	host, portText, _ := net.SplitHostPort(listener.Addr().String())
	var port int
	_, _ = fmt.Sscan(portText, &port)
	settings := map[string]any{"host": host, "port": port, "secure": false, "rejectUnauthorized": true, "fromAddress": "abs@example.test"}
	if err := sendMail(context.Background(), settings, "reader@example.test", "Test subject", "Success!", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-received:
		if !strings.Contains(msg, "Test subject") || !strings.Contains(msg, "Success!") {
			t.Fatalf("SMTP message %q", msg)
		}
	case err := <-errs:
		t.Fatal(err)
	}
}
