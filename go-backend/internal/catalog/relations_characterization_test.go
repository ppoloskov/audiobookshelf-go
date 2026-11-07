package catalog

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/internal/filterdata"
	"audiobookshelf-compat/internal/wire"
)

// relationSnapshot is everything one item mutation leaves behind: link and
// parent rows, the events it announced, the filter data patches and the
// metadata-file writes.
func relationSnapshot(t *testing.T, rig *authorRig) string {
	t.Helper()
	list := func(query string) string {
		rows, err := rig.db.Query(query)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return strings.Join(out, ",")
	}
	var names []string
	byName := map[string]string{}
	for _, e := range rig.takeEvents() {
		id := ""
		if m, ok := e.payload.(map[string]any); ok {
			id, _ = m["name"].(string)
			if id == "" {
				id, _ = m["id"].(string)
			}
		}
		names = append(names, e.event)
		byName[e.event] += id + ";"
	}
	sort.Strings(names)
	fd := rig.h.filters.Has("l1")
	filter := ""
	if fd {
		d, _ := rig.h.filters.Fresh("l1")
		var a, s []string
		for _, n := range d.Authors {
			a = append(a, n.Name)
		}
		for _, n := range d.Series {
			s = append(s, n.Name)
		}
		sort.Strings(a)
		sort.Strings(s)
		filter = strings.Join(a, ",") + "|" + strings.Join(s, ",")
	}
	touched := append([]string(nil), rig.touched...)
	sort.Strings(touched)
	return fmt.Sprintf("links=%s authors=%s series=%s seq=%s events=%s filter=%s touched=%s",
		list(`SELECT a.name FROM bookAuthors ba JOIN authors a ON a.id=ba.authorId WHERE ba.bookId='b1' ORDER BY ba.createdAt,ba.rowid`),
		list(`SELECT name FROM authors ORDER BY name`),
		list(`SELECT name FROM series ORDER BY name`),
		list(`SELECT s.name||'#'||COALESCE(bs.sequence,'') FROM bookSeries bs JOIN series s ON s.id=bs.seriesId WHERE bs.bookId='b1'`),
		strings.Join(names, ","), filter, strings.Join(touched, ","))
}

// TestItemMutationRelationsAcrossRoutes pins what PATCH media, batch update and
// quick match do to the author and series relations of one book (the packet C2
// characterisation table).
func TestItemMutationRelationsAcrossRoutes(t *testing.T) {
	type scenario struct {
		name    string
		prep    string
		authors []string
		series  [][2]string
		want    map[string]string
	}
	scenarios := []scenario{
		{name: "new author", authors: []string{"Ada Author", "Zed Newbie"}, series: [][2]string{{"First Series", "1"}}},
		{name: "existing author other case", authors: []string{"ben writer"}, series: [][2]string{{"First Series", "1"}}},
		{name: "last book removed", authors: []string{"Ben Writer"}, series: [][2]string{{"First Series", "1"}}},
		{name: "last book removed keeps asin author", prep: `UPDATE authors SET asin='B000' WHERE id='a1'`, authors: []string{"Ben Writer"}, series: [][2]string{{"First Series", "1"}}},
		{name: "series sequence change", authors: []string{"Ada Author"}, series: [][2]string{{"First Series", "2"}}},
		{name: "series replaced", authors: []string{"Ada Author"}, series: [][2]string{{"Other Series", "1"}}},
	}
	routes := []string{"patch", "batch", "quickmatch"}
	for _, sc := range scenarios {
		for _, route := range routes {
			t.Run(sc.name+"/"+route, func(t *testing.T) {
				finder := &fakeBooks{}
				rig := matchRig(t, finder)
				if sc.prep != "" {
					if _, err := rig.db.Exec(sc.prep); err != nil {
						t.Fatal(err)
					}
				}
				rig.h.filters.Store("l1", filterdata.Data{Authors: []filterdata.Named{{ID: "a1", Name: "Ada Author"}, {ID: "a2", Name: "Ben Writer"}}, Series: []filterdata.Named{{ID: "s1", Name: "First Series"}}, LoadedAt: time.Now().UnixMilli()})
				var authors, series []map[string]string
				for _, a := range sc.authors {
					authors = append(authors, map[string]string{"name": a})
				}
				for _, s := range sc.series {
					series = append(series, map[string]string{"name": s[0], "sequence": s[1]})
				}
				media := string(raw(map[string]any{"metadata": map[string]any{"authors": authors, "series": series}}))
				switch route {
				case "patch":
					if w := rig.do("PATCH", "/api/items/i1/media", media, rootUser()); w.Code != 200 {
						t.Fatalf("%d %s", w.Code, w.Body)
					}
				case "batch":
					if w := rig.do("POST", "/api/items/batch/update", `[{"id":"i1","mediaPayload":`+media+`}]`, rootUser()); w.Code != 200 {
						t.Fatalf("%d %s", w.Code, w.Body)
					}
				case "quickmatch":
					var seriesMatch []map[string]string
					for _, s := range sc.series {
						seriesMatch = append(seriesMatch, map[string]string{"series": s[0], "sequence": s[1]})
					}
					finder.books = []wire.BookMatch{{Author: raw(strings.Join(sc.authors, ", ")), Series: raw(seriesMatch)}}
					if w := rig.do("POST", "/api/items/i1/match", `{"overrideDetails":true,"overrideCover":false}`, rootUser()); w.Code != 200 {
						t.Fatalf("%d %s", w.Code, w.Body)
					}
				}
				got := relationSnapshot(t, rig)
				key := strings.ReplaceAll(sc.name, " ", "_") + "/" + route
				if want := wantRelations[key]; want != got {
					t.Fatalf("\n got %s\nwant %s", got, want)
				}
			})
		}
	}
}

// wantRelations was recorded from the code before the C2 refactor. The routes
// differ on purpose: quick match announces neither author_updated nor the
// reuse of an existing author, and adds series_added.
var wantRelations = map[string]string{
	"new_author/patch":                               "links=Ada Author,Zed Newbie authors=Ada Author,Ben Writer,Zed Newbie series=First Series seq=First Series#1 events=author_added,item_updated filter=Ada Author,Ben Writer,Zed Newbie|First Series touched=i1",
	"new_author/batch":                               "links=Ada Author,Zed Newbie authors=Ada Author,Ben Writer,Zed Newbie series=First Series seq=First Series#1 events=author_added,item_updated filter=Ada Author,Ben Writer,Zed Newbie|First Series touched=i1",
	"new_author/quickmatch":                          "links=Ada Author,Zed Newbie authors=Ada Author,Ben Writer,Zed Newbie series=First Series seq=First Series#1 events=author_added,item_updated filter=Ada Author,Ben Writer,Zed Newbie|First Series touched=i1",
	"existing_author_other_case/patch":               "links=Ben Writer authors=Ben Writer series=First Series seq=First Series#1 events=author_removed,author_updated,item_updated filter=Ben Writer|First Series touched=i1",
	"existing_author_other_case/batch":               "links=Ben Writer authors=Ben Writer series=First Series seq=First Series#1 events=author_removed,author_updated,item_updated filter=Ben Writer|First Series touched=i1",
	"existing_author_other_case/quickmatch":          "links=Ben Writer authors=Ben Writer series=First Series seq=First Series#1 events=author_removed,item_updated filter=Ben Writer|First Series touched=i1",
	"last_book_removed/patch":                        "links=Ben Writer authors=Ben Writer series=First Series seq=First Series#1 events=author_removed,author_updated,item_updated filter=Ben Writer|First Series touched=i1",
	"last_book_removed/batch":                        "links=Ben Writer authors=Ben Writer series=First Series seq=First Series#1 events=author_removed,author_updated,item_updated filter=Ben Writer|First Series touched=i1",
	"last_book_removed/quickmatch":                   "links=Ben Writer authors=Ben Writer series=First Series seq=First Series#1 events=author_removed,item_updated filter=Ben Writer|First Series touched=i1",
	"last_book_removed_keeps_asin_author/patch":      "links=Ben Writer authors=Ada Author,Ben Writer series=First Series seq=First Series#1 events=author_updated,item_updated filter=Ada Author,Ben Writer|First Series touched=i1",
	"last_book_removed_keeps_asin_author/batch":      "links=Ben Writer authors=Ada Author,Ben Writer series=First Series seq=First Series#1 events=author_updated,item_updated filter=Ada Author,Ben Writer|First Series touched=i1",
	"last_book_removed_keeps_asin_author/quickmatch": "links=Ben Writer authors=Ada Author,Ben Writer series=First Series seq=First Series#1 events=item_updated filter=Ada Author,Ben Writer|First Series touched=i1",
	"series_sequence_change/patch":                   "links=Ada Author authors=Ada Author,Ben Writer series=First Series seq=First Series#2 events=item_updated filter=Ada Author,Ben Writer|First Series touched=i1",
	"series_sequence_change/batch":                   "links=Ada Author authors=Ada Author,Ben Writer series=First Series seq=First Series#2 events=item_updated filter=Ada Author,Ben Writer|First Series touched=i1",
	"series_sequence_change/quickmatch":              "links=Ada Author authors=Ada Author,Ben Writer series=First Series seq=First Series#2 events=item_updated filter=Ada Author,Ben Writer|First Series touched=i1",
	"series_replaced/patch":                          "links=Ada Author authors=Ada Author,Ben Writer series=Other Series seq=Other Series#1 events=item_updated,series_removed filter=Ada Author,Ben Writer|Other Series touched=i1",
	"series_replaced/batch":                          "links=Ada Author authors=Ada Author,Ben Writer series=Other Series seq=Other Series#1 events=item_updated,series_removed filter=Ada Author,Ben Writer|Other Series touched=i1",
	"series_replaced/quickmatch":                     "links=Ada Author authors=Ada Author,Ben Writer series=Other Series seq=Other Series#1 events=item_updated,series_added,series_removed filter=Ada Author,Ben Writer|Other Series touched=i1",
}
