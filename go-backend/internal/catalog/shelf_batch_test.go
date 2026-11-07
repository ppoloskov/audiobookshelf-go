package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"audiobookshelf-compat/internal/config"
	"audiobookshelf-compat/internal/dbtime"
	"audiobookshelf-compat/internal/libitem"
	"audiobookshelf-compat/internal/store"
)

func TestBookShelfQueryCountIndependentOfSize(t *testing.T) {
	counts := make(map[int]int64, 2)
	for _, size := range []int{10, 50} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			db, counter := catalogFixtureWithQueryCount(t)
			seedBookPage(t, db, size)
			if _, err := db.Exec(`CREATE TABLE mediaProgresses (id TEXT PRIMARY KEY,userId TEXT,mediaItemId TEXT,currentTime FLOAT,ebookProgress FLOAT,isFinished BOOLEAN,hideFromContinueListening BOOLEAN,updatedAt DATETIME,createdAt DATETIME,finishedAt DATETIME)`); err != nil {
				t.Fatal(err)
			}
			base := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)
			for n := 2; n <= size*2; n++ {
				if n == 6 {
					// This item has progress only for another user.
					continue
				}
				itemID := fmt.Sprintf("i%d", n)
				updated := base.Add(time.Duration(n) * time.Hour)
				if n == 4 {
					updated = base.Add(1001 * time.Hour) // denied explicit content sorts first if ACL filtering regresses.
				}
				if n == 8 {
					updated = base.Add(1002 * time.Hour) // nullable title must remain in the result.
				}
				if _, err := db.Exec(`INSERT INTO mediaProgresses VALUES (?,?,?,1,0,0,0,?,?,NULL)`, "progress-"+itemID, "shelf-user", fmt.Sprintf("book-%02d", n), dbtime.Format(updated), dbtime.Format(base)); err != nil {
					t.Fatal(err)
				}
			}
			// Progress for another user must not leak into this user's shelf.
			if _, err := db.Exec(`INSERT INTO mediaProgresses VALUES ('other-user','other','book-06',1,0,0,0,?, ?,NULL)`, dbtime.Format(base.Add(1003*time.Hour)), dbtime.Format(base)); err != nil {
				t.Fatal(err)
			}
			h := New(db, config.Config{}, nil)
			user := store.User{ID: "shelf-user", IsActive: true, Permissions: json.RawMessage(`{"accessAllLibraries":true,"accessAllTags":true,"accessExplicitContent":false}`)}
			permission, permissionArgs := mediaPermission(user, "b")
			query := `SELECT li.id FROM libraryItems li JOIN books b ON b.id=li.mediaId JOIN mediaProgresses mp ON mp.mediaItemId=b.id WHERE li.libraryId=? AND li.mediaType='book' AND mp.userId=? AND mp.hideFromContinueListening=0 AND mp.isFinished=0 AND (mp.currentTime>0 OR mp.ebookProgress>0)` + permission + ` ORDER BY mp.updatedAt DESC LIMIT ?`
			args := []any{"l1", user.ID}
			args = append(args, permissionArgs...)
			args = append(args, size)
			r := httptest.NewRequest("GET", "/", nil)
			counter.reset()
			items, err := shelfItems(h, r, query, args)
			if err != nil {
				t.Fatal(err)
			}
			counts[size] = counter.reads()
			t.Logf("%d returned shelf items: %d SQL reads", len(items), counts[size])
			if len(items) != size {
				t.Fatalf("returned %d items, want %d", len(items), size)
			}
			gotIDs := make([]string, 0, len(items))
			for _, item := range items {
				gotIDs = append(gotIDs, item.ID)
				encoded, err := json.Marshal(item)
				if err != nil {
					t.Fatal(err)
				}
				var decoded map[string]any
				if err := json.Unmarshal(encoded, &decoded); err != nil {
					t.Fatal(err)
				}
				media := decoded["media"].(map[string]any)
				metadata := media["metadata"].(map[string]any)
				if metadata["titleIgnorePrefix"] == nil && metadata["title"] != nil {
					t.Errorf("item %s lacks title prefix: %#v", item.ID, metadata)
				}
				if decoded["libraryFiles"] != nil || decoded["numFiles"] != float64(0) {
					t.Errorf("item %s envelope files=%#v numFiles=%#v", item.ID, decoded["libraryFiles"], decoded["numFiles"])
				}
				if item.ID == "i8" && (metadata["title"] != nil || metadata["titleIgnorePrefix"] != nil) {
					t.Errorf("nullable title projection=%#v", metadata)
				}
				if item.ID == "i20" && (metadata["title"] != "The Book 020" || metadata["titleIgnorePrefix"] != "Book 020, The") {
					t.Errorf("title/prefix projection=%#v", metadata)
				}
			}
			wantIDs := []string{"i8"} // nullable-title item has highest eligible progress time.
			for n := size * 2; n >= 2 && len(wantIDs) < size; n-- {
				if n != 4 && n != 6 && n != 8 { // explicit and other-user-only progress are excluded.
					wantIDs = append(wantIDs, fmt.Sprintf("i%d", n))
				}
			}
			if !reflect.DeepEqual(gotIDs, wantIDs) {
				t.Fatalf("IDs=%v, want %v", gotIDs, wantIDs)
			}
			encoded, err := json.Marshal(items[0].Media)
			if err != nil {
				t.Fatal(err)
			}
			var firstMedia map[string]any
			if err := json.Unmarshal(encoded, &firstMedia); err != nil {
				t.Fatal(err)
			}
			if firstMedia["id"] != "book-08" {
				t.Fatalf("first media=%#v", firstMedia)
			}
		})
	}
	if counts[10] != counts[50] {
		t.Fatalf("query counts vary with shelf size: 10=%d, 50=%d", counts[10], counts[50])
	}

	db, _ := catalogFixtureWithQueryCount(t)
	seedBookPage(t, db, 10)
	h := New(db, config.Config{}, nil)
	r := httptest.NewRequest("GET", "/", nil)
	duplicates, err := shelfItems(h, r, `SELECT 'i8' UNION ALL SELECT 'i8'`, nil)
	if err != nil || len(duplicates) != 2 || duplicates[0].ID != "i8" || duplicates[1].ID != "i8" {
		t.Fatalf("duplicate selection result=%v err=%v", duplicates, err)
	}
	missing, err := shelfItems(h, r, `SELECT 'missing-item'`, nil)
	if !errors.Is(err, sql.ErrNoRows) || missing != nil {
		t.Fatalf("missing selection result=%v err=%v, want nil/sql.ErrNoRows", missing, err)
	}
}

func TestItemBatchQueryCounts(t *testing.T) {
	db, counter := catalogFixtureWithQueryCount(t)
	seedBookPage(t, db, 126) // 251 rows: one IN-list chunk (500) per table.

	for _, test := range []struct {
		name      string
		ids       []string
		wantReads int64
		wantRows  int
	}{
		// libitem.Load reads libraryItems, books, authors and series once each;
		// duplicate ids repeat their item in the result.
		{name: "empty", wantReads: 0, wantRows: 0},
		{name: "one", ids: []string{"i2"}, wantReads: 4, wantRows: 1},
		{name: "250 with duplicate", ids: append(itemIDsForShelf(250), "i2"), wantReads: 4, wantRows: 251},
		{name: "251", ids: itemIDsForShelf(251), wantReads: 4, wantRows: 251},
		{name: "missing", ids: []string{"missing"}, wantReads: 1, wantRows: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			counter.reset()
			got, err := libitem.Load(context.Background(), db, test.ids, libitem.Options{})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != test.wantRows {
				t.Fatalf("libitem.Load returned %d rows, want %d", len(got), test.wantRows)
			}
			if reads := counter.reads(); reads != test.wantReads {
				t.Fatalf("libitem.Load issued %d reads, want %d", reads, test.wantReads)
			}
		})
	}
}

func itemIDsForShelf(count int) []string {
	ids := make([]string, count)
	for i := range ids {
		ids[i] = fmt.Sprintf("i%d", i+2)
	}
	return ids
}

// shelfItems is a shelf's two steps: the ids a query selects, then one batched
// load of them.
func shelfItems(h *Handler, r *http.Request, query string, args []any) ([]libitem.Minified, error) {
	ids, err := h.queryStrings(r.Context(), query, args...)
	if err != nil {
		return nil, err
	}
	return h.minifiedItems(r.Context(), ids)
}
