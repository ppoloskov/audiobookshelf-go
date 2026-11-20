package oracle

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"testing"
)

func TestExpressPathToOpenAPI(t *testing.T) {
	tests := []struct {
		in         string
		wantPath   string
		wantParams []string
	}{
		{
			in:         "/",
			wantPath:   "/",
			wantParams: nil,
		},
		{
			in:         "/api/libraries/:id/items",
			wantPath:   "/api/libraries/{id}/items",
			wantParams: []string{"id"},
		},
		{
			in:         "/api/items/:id/ebook/:fileid?",
			wantPath:   "/api/items/{id}/ebook/{fileid}",
			wantParams: []string{"id", "fileid"},
		},
		{
			in:         "/feed/:slug/cover*",
			wantPath:   "/feed/{slug}/cover{wildcard}",
			wantParams: []string{"slug", "wildcard"},
		},
		{
			in:         "/*",
			wantPath:   "/{wildcard}",
			wantParams: []string{"wildcard"},
		},
	}
	for _, tc := range tests {
		gotPath, gotParams := ExpressPathToOpenAPI(tc.in)
		if gotPath != tc.wantPath {
			t.Errorf("ExpressPathToOpenAPI(%q) path = %q, want %q", tc.in, gotPath, tc.wantPath)
		}
		if !slices.Equal(gotParams, tc.wantParams) {
			t.Errorf("ExpressPathToOpenAPI(%q) params = %v, want %v", tc.in, gotParams, tc.wantParams)
		}
	}
}

func TestInferAndMergeSchema(t *testing.T) {
	s1, ok := InferJSONSchema([]byte(`{"id":"u1","count":3,"rating":null,"tags":[]}`))
	if !ok {
		t.Fatal("InferJSONSchema s1 failed")
	}
	s2, ok := InferJSONSchema([]byte(`{"id":"u2","count":3.5,"rating":"good","tags":["sci-fi"],"extra":true}`))
	if !ok {
		t.Fatal("InferJSONSchema s2 failed")
	}
	merged := finalizeSchema(MergeSchema(s1, s2))

	if merged.Type != "object" {
		t.Fatalf("merged.Type = %q, want object", merged.Type)
	}
	if merged.Properties["id"].Type != "string" {
		t.Errorf("id type = %q, want string", merged.Properties["id"].Type)
	}
	if merged.Properties["count"].Type != "number" {
		t.Errorf("count type = %q, want number (integer+number)", merged.Properties["count"].Type)
	}
	if merged.Properties["rating"].Type != "string" || !merged.Properties["rating"].Nullable {
		t.Errorf("rating = %+v, want nullable string", merged.Properties["rating"])
	}
	if merged.Properties["tags"].Type != "array" || merged.Properties["tags"].Items == nil || merged.Properties["tags"].Items.Type != "string" {
		t.Errorf("tags = %+v, want array of string", merged.Properties["tags"])
	}
	if merged.Properties["extra"].Type != "boolean" {
		t.Errorf("extra type = %q, want boolean", merged.Properties["extra"].Type)
	}

	// Empty array finalized on its own gets empty items schema {}.
	emptyArr, ok := InferJSONSchema([]byte(`[]`))
	if !ok {
		t.Fatal("InferJSONSchema empty array failed")
	}
	finalEmpty := finalizeSchema(emptyArr)
	if finalEmpty.Type != "array" || finalEmpty.Items == nil {
		t.Fatalf("finalEmpty = %+v, want array with non-nil items", finalEmpty)
	}

	// Depth bound: deeper than maxSchemaDepth stops expanding nested properties.
	deep := []byte(`{"a":{"b":{"c":{"d":{"e":{"f":{"g":{"h":1}}}}}}}}`)
	deepSchema, ok := InferJSONSchema(deep)
	if !ok {
		t.Fatal("InferJSONSchema deep failed")
	}
	cur := finalizeSchema(deepSchema)
	for _, key := range []string{"a", "b", "c", "d", "e", "f"} {
		if cur == nil || cur.Properties[key] == nil {
			t.Fatalf("missing expected nested key %q at depth", key)
		}
		cur = cur.Properties[key]
	}
	if cur.Type != "object" || len(cur.Properties) != 0 {
		t.Errorf("expected depth-capped object without properties at depth %d, got %+v", maxSchemaDepth, cur)
	}
}

func TestOpenAPIBuildDocumentFromManifest(t *testing.T) {
	manifest, err := LoadRouteManifest("routes.json")
	if err != nil {
		t.Fatalf("LoadRouteManifest: %v", err)
	}

	rec := NewOpenAPIRecorder()
	// Non-TS call must be ignored.
	rec.Record(HTTPObservation{
		IsTS:            false,
		Method:          http.MethodGet,
		Path:            "/audiobookshelf/api/libraries",
		Status:          http.StatusOK,
		RespContentType: "application/json",
		RespBody:        []byte(`{"ignored":true}`),
	})
	// Observed TS GET with query param and JSON response.
	rec.Record(HTTPObservation{
		IsTS:            true,
		Method:          http.MethodGet,
		Path:            "/audiobookshelf/api/libraries/lib-1/items",
		Query:           url.Values{"limit": {"10"}, "sort": {"addedAt"}},
		Status:          http.StatusOK,
		RespContentType: "application/json; charset=utf-8",
		RespBody:        []byte(`{"results":[{"id":"item-1"}],"total":1}`),
	})
	// Observed TS POST with JSON body + 200 and 400 responses.
	rec.Record(HTTPObservation{
		IsTS:            true,
		Method:          http.MethodPost,
		Path:            "/audiobookshelf/api/collections",
		ReqContentType:  "application/json",
		ReqBody:         []byte(`{"libraryId":"lib-1","name":"Favorites"}`),
		Status:          http.StatusOK,
		RespContentType: "application/json",
		RespBody:        []byte(`{"id":"col-1","name":"Favorites"}`),
	})
	rec.Record(HTTPObservation{
		IsTS:            true,
		Method:          http.MethodPost,
		Path:            "/audiobookshelf/api/collections",
		ReqContentType:  "application/json",
		ReqBody:         []byte(`{not-json`),
		Status:          http.StatusBadRequest,
		RespContentType: "text/plain",
		RespBody:        []byte(`Invalid request body`),
	})

	doc := rec.BuildDocument(manifest)
	if doc.OpenAPI != "3.0.3" {
		t.Errorf("doc.OpenAPI = %q, want 3.0.3", doc.OpenAPI)
	}
	if doc.Info.Title != "Monstrobook / Audiobookshelf API" || doc.Info.Version != "2.37.2" {
		t.Errorf("unexpected doc.Info: %+v", doc.Info)
	}
	if _, ok := doc.Components.SecuritySchemes["bearerAuth"]; !ok {
		t.Error("missing bearerAuth security scheme")
	}

	// Count all operations across paths; must match all routes in routes.json.
	opCount := 0
	for _, methods := range doc.Paths {
		opCount += len(methods)
	}
	if opCount != len(manifest.Routes) {
		t.Errorf("operation count = %d, want %d", opCount, len(manifest.Routes))
	}

	itemsOp := doc.Paths["/api/libraries/{id}/items"]["get"]
	if itemsOp == nil {
		t.Fatal("missing GET /api/libraries/{id}/items operation")
	}
	if !slices.Equal(itemsOp.Tags, []string{"libraries"}) {
		t.Errorf("itemsOp.Tags = %v, want [libraries]", itemsOp.Tags)
	}
	if len(itemsOp.Parameters) != 3 {
		t.Fatalf("itemsOp.Parameters len = %d, want 3 (1 path + 2 query)", len(itemsOp.Parameters))
	}
	if itemsOp.Parameters[0].In != "path" || itemsOp.Parameters[0].Name != "id" || !itemsOp.Parameters[0].Required {
		t.Errorf("unexpected path param: %+v", itemsOp.Parameters[0])
	}
	if itemsOp.Parameters[1].In != "query" || itemsOp.Parameters[1].Name != "limit" || itemsOp.Parameters[2].Name != "sort" {
		t.Errorf("unexpected query params: %+v", itemsOp.Parameters[1:])
	}

	colOp := doc.Paths["/api/collections"]["post"]
	if colOp == nil {
		t.Fatal("missing POST /api/collections operation")
	}
	if colOp.RequestBody == nil || colOp.RequestBody.Content["application/json"].Schema == nil {
		t.Fatal("missing requestBody JSON schema on POST /api/collections")
	}
	reqProps := colOp.RequestBody.Content["application/json"].Schema.Properties
	if reqProps["libraryId"].Type != "string" || reqProps["name"].Type != "string" {
		t.Errorf("unexpected requestBody props: %+v", reqProps)
	}
	if _, ok := colOp.Responses["200"]; !ok {
		t.Error("missing 200 response on POST /api/collections")
	}
	if resp400, ok := colOp.Responses["400"]; !ok || resp400.Content["text/plain"].Schema.Type != "string" {
		t.Errorf("unexpected 400 response on POST /api/collections: %+v", resp400)
	}

	// Unhit route gets default 200 OK response.
	pingOp := doc.Paths["/ping"]["get"]
	if pingOp == nil {
		t.Fatal("missing GET /ping operation")
	}
	if len(pingOp.Security) != 0 {
		t.Errorf("GET /ping should be public (no security), got %v", pingOp.Security)
	}
	if _, ok := pingOp.Responses["200"]; !ok {
		t.Error("unhit GET /ping missing fallback 200 response")
	}

	// Verify full JSON round-trip marshaling succeeds cleanly.
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal OpenAPI doc: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal OpenAPI doc: %v", err)
	}
	if decoded["openapi"] != "3.0.3" {
		t.Errorf("encoded openapi = %v, want 3.0.3", decoded["openapi"])
	}
}
