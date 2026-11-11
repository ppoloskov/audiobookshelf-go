package db_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"audiobookshelf-compat/internal/db"
	"audiobookshelf-compat/internal/db/dbtest"
)

const wantMillis = int64(1704067200123)

func TestTimeScan(t *testing.T) {
	at := time.UnixMilli(wantMillis)
	cases := []struct {
		name    string
		src     any
		want    int64 // Unix milliseconds; ignored when zero is true
		zero    bool
		wantErr bool
	}{
		{name: "nil", src: nil, zero: true},
		{name: "time.Time", src: at, want: wantMillis},
		{name: "sequelize text", src: "2024-01-01 00:00:00.123 +00:00", want: wantMillis},
		{name: "nanoseconds", src: "2024-01-01 00:00:00.123456789 +00:00", want: wantMillis},
		{name: "no space before offset", src: "2024-01-01 00:00:00.123+00:00", want: wantMillis},
		{name: "no offset is UTC", src: "2024-01-01 00:00:00.123", want: wantMillis},
		{name: "RFC 3339", src: "2024-01-01T00:00:00.123Z", want: wantMillis},
		{name: "other offset", src: "2024-01-01 02:00:00.123 +02:00", want: wantMillis},
		{name: "no milliseconds", src: "2024-01-01 00:00:00 +00:00", want: 1704067200000},
		{name: "no milliseconds no offset", src: "2024-01-01 00:00:00", want: 1704067200000},
		{name: "bytes", src: []byte("2024-01-01 00:00:00.123 +00:00"), want: wantMillis},
		{name: "padded", src: " 2024-01-01 00:00:00.123 +00:00\n", want: wantMillis},
		{name: "digits are millis", src: "1704067200123", want: wantMillis},
		{name: "int64 millis", src: wantMillis, want: wantMillis},
		{name: "float64 millis", src: float64(wantMillis), want: wantMillis},
		{name: "empty text", src: "", zero: true},
		{name: "blank text", src: "  ", zero: true},
		{name: "garbage", src: "yesterday", wantErr: true},
		{name: "bool", src: true, wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := db.FromMillis(5) // overwritten, or reset to zero
			err := got.Scan(c.src)
			if (err != nil) != c.wantErr {
				t.Fatalf("Scan(%#v) error = %v, want error %v", c.src, err, c.wantErr)
			}
			switch {
			case c.wantErr:
			case c.zero && !got.IsZero():
				t.Fatalf("Scan(%#v) = %v, want zero", c.src, got)
			case !c.zero && got.Millis() != c.want:
				t.Fatalf("Scan(%#v) = %d, want %d", c.src, got.Millis(), c.want)
			}
		})
	}
}

func TestTimeValue(t *testing.T) {
	got, err := db.FromMillis(wantMillis).Value()
	if err != nil || got != "2024-01-01 00:00:00.123 +00:00" {
		t.Fatalf("Value = %#v, %v", got, err)
	}
	// A non-UTC time is written in UTC.
	zone := time.FixedZone("x", 2*3600)
	got, _ = db.Time{Time: time.UnixMilli(wantMillis).In(zone)}.Value()
	if got != "2024-01-01 00:00:00.123 +00:00" {
		t.Fatalf("Value of +02:00 time = %#v", got)
	}
	if got, err := (db.Time{}).Value(); got != nil || err != nil {
		t.Fatalf("zero Value = %#v, %v; want NULL", got, err)
	}
	if now := db.Now(); now.Nanosecond()%int(time.Millisecond) != 0 {
		t.Fatalf("Now is not truncated to the millisecond: %v", now)
	}
}

func TestTimeJSON(t *testing.T) {
	data, err := json.Marshal(struct{ A, B db.Time }{A: db.FromMillis(wantMillis)})
	if err != nil || string(data) != `{"A":1704067200123,"B":null}` {
		t.Fatalf("Marshal = %s, %v", data, err)
	}
	var got struct{ A, B, C db.Time }
	if err := json.Unmarshal([]byte(`{"A":1704067200123,"B":null,"C":1.5}`), &got); err != nil {
		t.Fatal(err)
	}
	if got.A.Millis() != wantMillis || !got.B.IsZero() || got.C.Millis() != 1 {
		t.Fatalf("Unmarshal = %+v", got)
	}
	if err := json.Unmarshal([]byte(`"2024"`), &got); err == nil {
		t.Fatal("a string is not a time in JSON")
	}
}

func TestBool(t *testing.T) {
	for _, c := range []struct {
		src     any
		want    db.Bool
		wantErr bool
	}{{nil, false, false}, {int64(0), false, false}, {int64(1), true, false}, {true, true, false}, {float64(1), true, false}, {"1", false, true}} {
		got := db.Bool(true)
		err := got.Scan(c.src)
		if (err != nil) != c.wantErr || (err == nil && got != c.want) {
			t.Errorf("Scan(%#v) = %v, %v", c.src, got, err)
		}
	}
	if v, _ := db.Bool(true).Value(); v != int64(1) {
		t.Errorf("true Value = %#v", v)
	}
	if v, _ := db.Bool(false).Value(); v != int64(0) {
		t.Errorf("false Value = %#v", v)
	}
}

func TestPtrVal(t *testing.T) {
	if *db.Ptr(3) != 3 || db.Val(db.Ptr("a")) != "a" || db.Val[int](nil) != 0 {
		t.Fatal("Ptr/Val")
	}
}

func TestJSONScan(t *testing.T) {
	cases := []struct {
		name string
		src  any
		want []string
	}{
		{name: "NULL", src: nil, want: nil},
		{name: "empty text", src: "", want: nil},
		{name: "malformed", src: "[not json", want: nil},
		{name: "wrong type", src: `{"a":1}`, want: nil},
		{name: "partly wrong type", src: `["a",1]`, want: nil},
		{name: "string", src: `["a","b"]`, want: []string{"a", "b"}},
		{name: "bytes", src: []byte(`["a"]`), want: []string{"a"}},
		{name: "empty array", src: `[]`, want: []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			j := db.JSON[[]string]{V: []string{"stale"}}
			if err := j.Scan(c.src); err != nil {
				t.Fatalf("Scan: %v", err)
			}
			if !reflect.DeepEqual(j.V, c.want) {
				t.Fatalf("Scan(%#v) = %#v, want %#v", c.src, j.V, c.want)
			}
		})
	}
	if err := new(db.JSON[[]string]).Scan(int64(1)); err == nil {
		t.Fatal("an integer cell is not JSON text")
	}
}

type doc struct {
	N int    `json:"n"`
	S string `json:"s,omitempty"`
}

func TestJSONValue(t *testing.T) {
	cases := []struct {
		name string
		got  func() (driver.Value, error)
		want any
	}{
		{"nil slice", db.JSON[[]string]{}.Value, nil},
		{"empty slice", db.JSON[[]string]{V: []string{}}.Value, "[]"},
		{"slice", db.JSON[[]string]{V: []string{"a", "b"}}.Value, `["a","b"]`},
		{"nil map", db.JSON[map[string]int]{}.Value, nil},
		{"nil pointer", db.JSON[*doc]{}.Value, nil},
		{"pointer", db.JSON[*doc]{V: &doc{N: 1}}.Value, `{"n":1}`},
		{"struct is always text", db.JSON[doc]{}.Value, `{"n":0}`},
		{"empty raw", db.JSON[json.RawMessage]{}.Value, nil},
		{"raw is compacted", db.JSON[json.RawMessage]{V: json.RawMessage(` { "a" : 1 } `)}.Value, `{"a":1}`},
		{"number", db.JSON[int]{V: 0}.Value, "0"},
	}
	for _, c := range cases {
		got, err := c.got()
		if err != nil || got != c.want {
			t.Errorf("%s: Value = %#v, %v; want %#v", c.name, got, err, c.want)
		}
	}
}

func TestJSONMarshalDelegates(t *testing.T) {
	data, err := json.Marshal(struct{ D db.JSON[doc] }{db.JSON[doc]{V: doc{N: 2, S: "x"}}})
	if err != nil || string(data) != `{"D":{"n":2,"s":"x"}}` {
		t.Fatalf("Marshal = %s, %v", data, err)
	}
	var got struct{ D db.JSON[doc] }
	if err := json.Unmarshal(data, &got); err != nil || got.D.V != (doc{N: 2, S: "x"}) {
		t.Fatalf("Unmarshal = %+v, %v", got, err)
	}
}

// TestJSONIsStoredAsText proves a JSON column is written as TEXT, not a BLOB,
// and that NULL and the document both read back.
func TestJSONIsStoredAsText(t *testing.T) {
	d := dbtest.New(t)
	ctx := context.Background()
	user := db.User{ID: "u1", CreatedAt: db.Now(), UpdatedAt: db.Now(),
		Permissions: db.JSON[db.Permissions]{V: db.Permissions{Download: true}},
		Bookmarks:   db.JSON[[]db.Bookmark]{V: []db.Bookmark{{LibraryItemID: "li", Time: 1.5, Title: "t", CreatedAt: 9}}},
	}
	if err := d.Insert(ctx, "users", user); err != nil {
		t.Fatal(err)
	}
	var types struct {
		Permissions string `db:"permissions"`
		Bookmarks   string `db:"bookmarks"`
		ExtraData   string `db:"extraData"`
	}
	if err := d.Get(ctx, &types, "SELECT typeof(permissions) AS permissions, typeof(bookmarks) AS bookmarks, typeof(extraData) AS extraData FROM users"); err != nil {
		t.Fatal(err)
	}
	if types.Permissions != "text" || types.Bookmarks != "text" || types.ExtraData != "null" {
		t.Fatalf("storage types = %+v", types)
	}
	var got db.User
	if err := d.Get(ctx, &got, "SELECT * FROM users WHERE id = ?", "u1"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, user) {
		t.Fatalf("read back %+v, want %+v", got, user)
	}
	if err := d.Get(ctx, &got, "SELECT * FROM users WHERE id = ?", "missing"); err != sql.ErrNoRows {
		t.Fatalf("missing row error = %v, want sql.ErrNoRows", err)
	}
}
