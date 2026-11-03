package identity

import (
	"net/http/httptest"
	"strings"
	"testing"

	"audiobookshelf-compat/internal/logging"
)

func loggedMessages(prefix string) []string {
	var out []string
	for _, e := range logging.Default().Recent() {
		if strings.HasPrefix(e.Message, prefix) {
			out = append(out, e.LevelName+" "+e.Message)
		}
	}
	return out
}

// Login outcomes are logged with LocalAuthStrategy's wording
// (server/auth/LocalAuthStrategy.js:74,104).
func TestLocalLoginLogsSuccessAndFailure(t *testing.T) {
	db, handler := identityFixture(t)
	defer db.Close()
	post := func(body string) int {
		req := httptest.NewRequest("POST", "/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "203.0.113.9:4444"
		w := httptest.NewRecorder()
		handler.Handle(w, req)
		return w.Code
	}
	if code := post(`{"username":"reader","password":"wrong"}`); code != 401 {
		t.Fatalf("wrong password status %d", code)
	}
	if code := post(`{"username":"nobody","password":"x"}`); code != 401 {
		t.Fatalf("unknown user status %d", code)
	}
	if code := post(`{"username":"reader","password":"shelf"}`); code != 200 {
		t.Fatalf("login status %d", code)
	}
	failed := loggedMessages("[LocalAuth] Failed login attempt")
	wantFailed := []string{
		`ERROR [LocalAuth] Failed login attempt for username "reader" from ip 203.0.113.9 (Invalid password)`,
		`ERROR [LocalAuth] Failed login attempt for username "nobody" from ip 203.0.113.9 (User not found)`,
	}
	for _, want := range wantFailed {
		found := false
		for _, got := range failed {
			found = found || got == want
		}
		if !found {
			t.Errorf("missing log line %q in %q", want, failed)
		}
	}
	ok := loggedMessages(`[LocalAuth] User "reader" logged in from ip 203.0.113.9`)
	if len(ok) == 0 || !strings.HasPrefix(ok[len(ok)-1], "INFO ") {
		t.Errorf("success line missing: %q", ok)
	}
}
