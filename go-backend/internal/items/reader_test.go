package items

import (
	"reflect"
	"testing"

	"audiobookshelf-compat/internal/wire"
)

func TestAccessMediaTypedBookShapes(t *testing.T) {
	full := wire.BookFull{
		ID:       "book-1",
		Metadata: wire.BookMetadataFull{Explicit: true},
		Tags:     []string{"mystery"},
	}
	minified := wire.BookMinified{
		ID:       "book-2",
		Metadata: wire.BookMetadataMinified{Explicit: false},
		Tags:     []string{"romance"},
	}
	expanded := wire.BookExpanded{
		ID:       "book-3",
		Metadata: wire.BookMetadataExpanded{BookMetadataFull: wire.BookMetadataFull{Explicit: true}},
		Tags:     []string{"history"},
	}
	for name, test := range map[string]struct {
		media any
		want  MediaAccess
	}{
		"full":     {media: full, want: MediaAccess{ID: "book-1", Explicit: true, Tags: []string{"mystery"}}},
		"minified": {media: minified, want: MediaAccess{ID: "book-2", Tags: []string{"romance"}}},
		"expanded": {media: expanded, want: MediaAccess{ID: "book-3", Explicit: true, Tags: []string{"history"}}},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := AccessMedia(test.media)
			if !ok {
				t.Fatal("typed book media was not recognized")
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("AccessMedia() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestAccessMediaLegacyMapAndMissingMedia(t *testing.T) {
	media := map[string]any{
		"id":       "podcast-1",
		"explicit": true,
		"tags":     []any{"comedy", "interview"},
	}
	got, ok := AccessMedia(media)
	if !ok {
		t.Fatal("legacy media map was not recognized")
	}
	want := MediaAccess{ID: "podcast-1", Explicit: true, Tags: []string{"comedy", "interview"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AccessMedia() = %#v, want %#v", got, want)
	}
	if _, ok := AccessMedia(nil); ok {
		t.Fatal("nil media must not be recognized")
	}
	if _, ok := AccessMedia(struct{}{}); ok {
		t.Fatal("unrecognized media must not be recognized")
	}
}
