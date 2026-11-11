package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type payload struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func TestDecode(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		limit  int64
		want   payload
		status int // 0 means success
	}{
		{"ok", `{"name":"a","count":2}`, 0, payload{"a", 2}, 0},
		{"unknown fields allowed", `{"name":"a","extra":[1,2]}`, 0, payload{Name: "a"}, 0},
		{"trailing whitespace", "{\"name\":\"a\"}\n \n", 0, payload{Name: "a"}, 0},
		{"empty", ``, 0, payload{}, 400},
		{"blank", "  \n", 0, payload{}, 400},
		{"malformed", `{"name":`, 0, payload{}, 400},
		{"not json", `hello`, 0, payload{}, 400},
		{"wrong type", `{"count":"many"}`, 0, payload{}, 400},
		{"trailing garbage", `{"name":"a"} x`, 0, payload{}, 400},
		{"second value", `{"name":"a"}{"name":"b"}`, 0, payload{}, 400},
		{"too large", `{"name":"` + strings.Repeat("a", 100) + `"}`, 32, payload{}, 413},
		{"too large in trailing data", `{"name":"a"}` + strings.Repeat(" ", 64), 32, payload{}, 413},
		{"zero limit", `{}`, -1, payload{}, 500},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
			var got payload
			var err error
			if tc.limit == 0 {
				got, err = Decode[payload](httptest.NewRecorder(), r)
			} else {
				got, err = DecodeLimit[payload](httptest.NewRecorder(), r, tc.limit)
			}
			if tc.status == 0 {
				if err != nil || got != tc.want {
					t.Fatalf("got %+v, %v", got, err)
				}
				return
			}
			rec := httptest.NewRecorder()
			Handler(func(http.ResponseWriter, *http.Request) error { return err }).ServeHTTP(rec, httptest.NewRequest("POST", "/", nil))
			if err == nil || rec.Code != tc.status {
				t.Fatalf("err = %v, status = %d, want %d", err, rec.Code, tc.status)
			}
		})
	}
}

func TestDecodeDefaultLimitIsOneMiB(t *testing.T) {
	body := `{"name":"` + strings.Repeat("a", 1<<20) + `"}`
	_, err := Decode[payload](httptest.NewRecorder(), httptest.NewRequest("POST", "/", strings.NewReader(body)))
	rec := httptest.NewRecorder()
	Handler(func(http.ResponseWriter, *http.Request) error { return err }).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 413 {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestQuery(t *testing.T) {
	r := httptest.NewRequest("GET", "/?limit=25&bad=x&neg=-3&empty=&a=1&b=true&c=false&d=yes", nil)
	for key, want := range map[string]int{"limit": 25, "bad": 7, "neg": -3, "empty": 7, "missing": 7} {
		if got := QueryInt(r, key, 7); got != want {
			t.Errorf("QueryInt(%q) = %d, want %d", key, got, want)
		}
	}
	for key, want := range map[string]bool{"a": true, "b": true, "c": false, "d": false, "missing": false} {
		if got := QueryBool(r, key); got != want {
			t.Errorf("QueryBool(%q) = %v, want %v", key, got, want)
		}
	}
}
