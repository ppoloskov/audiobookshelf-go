package store

import (
	"context"
	"database/sql"
	"testing"

	"audiobookshelf-compat/internal/deviceinfo"

	_ "modernc.org/sqlite"
)

func TestUpsertReusesRowPerDeviceAndUser(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE devices (id TEXT PRIMARY KEY,deviceId TEXT,clientName TEXT,clientVersion TEXT,ipAddress TEXT,deviceName TEXT,deviceVersion TEXT,extraData TEXT,userId TEXT,createdAt TEXT,updatedAt TEXT)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	user := Device{DeviceID: "dev", UserID: "u1", ClientName: "Abs Web", Info: deviceinfo.Info{OSName: "Linux", Browser: "Chrome"}}
	first, _, err := UpsertDevice(ctx, db, user)
	if err != nil {
		t.Fatal(err)
	}
	user.ClientName = "renamed"
	second, _, err := UpsertDevice(ctx, db, user)
	if err != nil || second != first {
		t.Fatalf("same device and user must reuse the row: %q %q %v", first, second, err)
	}
	shared := Device{DeviceID: "dev", ClientName: "Abs Web Share"}
	third, _, err := UpsertDevice(ctx, db, shared)
	if err != nil || third == first {
		t.Fatalf("public visit must not reuse a user's row: %q %q %v", first, third, err)
	}
	if again, _, _ := UpsertDevice(ctx, db, shared); again != third {
		t.Fatal("public visit must reuse its own row")
	}
	var name, extra string
	if err := db.QueryRow(`SELECT clientName,extraData FROM devices WHERE id=?`, first).Scan(&name, &extra); err != nil || name != "renamed" || extra != `{"osName":"Linux","browserName":"Chrome"}` {
		t.Fatalf("row = %q %q %v", name, extra, err)
	}
	if _, id, _ := UpsertDevice(ctx, db, Device{UserID: "u1"}); id == "" {
		t.Fatal("empty device id must default to the row id")
	}
}
