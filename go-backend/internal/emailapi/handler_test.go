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
