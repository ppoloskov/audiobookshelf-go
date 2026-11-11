package contracttest

import (
	"net/http"
	"testing"
)

func TestListsRouteBaseline(t *testing.T) {
	in := Boot(t)
	root := in.Login(t, in.Users["root"].Username, in.Users["root"].Password)
	listener := in.Login(t, in.Users["listener"].Username, in.Users["listener"].Password)
	client := *in.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	in.client = &client

	collection := in.IDs["collection"]
	playlist := in.IDs["playlist"]
	for _, base := range []string{"", routerBase} {
		for _, tc := range []struct {
			path, token string
			status      int
			media       string
			body        string
		}{
			{"/api/collections", root, 200, "application/json", ""},
			{"/api/collections/", root, 200, "application/json", ""},
			{"/api/collections//", root, 200, "application/json", ""},
			{"/api/collections/" + collection, root, 200, "application/json", ""},
			{"/api/collections/" + collection + "/", root, 200, "application/json", ""},
			{"/api/collections/" + collection + "//", root, 404, "text/plain", "Not Found"},
			{"/api/collections/" + collection + "/unknown", root, 404, "text/plain", "Not Found"},
			{"/api/playlists", root, 200, "application/json", ""},
			{"/api/playlists/", root, 200, "application/json", ""},
			{"/api/playlists//", root, 200, "application/json", ""},
			{"/api/playlists/" + playlist, root, 403, "text/plain", "Forbidden"},
			{"/api/playlists/" + playlist + "/", root, 403, "text/plain", "Forbidden"},
			{"/api/playlists/" + playlist + "//", root, 404, "text/plain", "Not Found"},
			{"/api/playlists/" + playlist + "/item/extra/segment", root, 404, "text/plain", "Not Found"},
		} {
			requestPath := base + tc.path
			resp, body := in.Do(t, http.MethodGet, requestPath, tc.token, nil)
			if resp.StatusCode != tc.status || mediaType(resp.Header.Get("Content-Type")) != tc.media || resp.Header.Get("Location") != "" || resp.Request.URL.Path != requestPath || (tc.body != "" && string(body) != tc.body) {
				t.Errorf("GET %s: status=%d media=%s location=%q finalPath=%s; want status=%d media=%s without redirect", requestPath, resp.StatusCode, mediaType(resp.Header.Get("Content-Type")), resp.Header.Get("Location"), resp.Request.URL.Path, tc.status, tc.media)
			}
		}
	}
	for _, tc := range []struct {
		method, path, token, body string
		status                    int
		media, responseBody       string
	}{
		{http.MethodPost, "/api/collections/" + collection + "/batch/add", root, `{"books":["missing-item"]}`, 400, "text/plain", "Invalid request body. No valid books"},
		{http.MethodPost, "/api/collections/" + collection + "/batch/add/", root, `{"books":["missing-item"]}`, 400, "text/plain", "Invalid request body. No valid books"},
		{http.MethodPost, "/api/collections/" + collection + "/batch/add//", root, `{"books":["missing-item"]}`, 404, "text/plain", "Not Found"},
		{http.MethodPost, "/api/playlists/" + playlist + "/batch/add", listener, `{"items":[]}`, 400, "text/plain", "Invalid request body items"},
		{http.MethodPost, "/api/playlists/" + playlist + "/batch/add/", listener, `{"items":[]}`, 400, "text/plain", "Invalid request body items"},
		{http.MethodPost, "/api/playlists/" + playlist + "/batch/add//", listener, `{"items":[]}`, 404, "text/plain", "Not Found"},
		{http.MethodHead, "/api/collections", root, "", 404, "text/plain", ""},
		{http.MethodOptions, "/api/collections", root, "", 404, "text/plain", "Not Found"},
		{http.MethodPut, "/api/playlists/" + playlist, listener, "", 404, "text/plain", "Not Found"},
	} {
		var body any
		if tc.body != "" {
			body = rawBody{contentType: "application/json", data: []byte(tc.body)}
		}
		resp, responseBody := in.Do(t, tc.method, routerBase+tc.path, tc.token, body)
		if resp.StatusCode != tc.status || mediaType(resp.Header.Get("Content-Type")) != tc.media || resp.Header.Get("Location") != "" || resp.Request.URL.Path != routerBase+tc.path || string(responseBody) != tc.responseBody {
			t.Errorf("%s %s: status=%d media=%s location=%q finalPath=%s body=%q; want status=%d media=%s body=%q without redirect", tc.method, tc.path, resp.StatusCode, mediaType(resp.Header.Get("Content-Type")), resp.Header.Get("Location"), resp.Request.URL.Path, responseBody, tc.status, tc.media, tc.responseBody)
		}
	}
}
