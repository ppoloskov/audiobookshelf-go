package contracttest

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestEmptyTagAllowListSeesNothing pins the list/item agreement for a user
// whose allow list is empty (accessAllTags false, no tags selected): the item
// check denies every item (User.checkCanAccessLibraryItemWithTags), so the
// library list must not return any either. Upstream's list query skips its tag
// filter in this case; Go fails closed (docs/SIMPLIFICATION.md, M7 fixes).
func TestEmptyTagAllowListSeesNothing(t *testing.T) {
	in := Boot(t)
	root := in.Login(t, in.Users["root"].Username, in.Users["root"].Password)
	body := map[string]any{
		"username": "empty-allowlist", "password": "empty-allowlist-password", "type": "user", "isActive": true,
		"permissions": map[string]any{"download": true, "accessAllLibraries": true, "accessAllTags": false,
			"itemTagsSelected": []string{}, "selectedTagsNotAccessible": false, "accessExplicitContent": true},
	}
	if resp, raw := in.Do(t, http.MethodPost, "/api/users", root, body); resp.StatusCode != http.StatusOK {
		t.Fatalf("create user: %d %s", resp.StatusCode, raw)
	}
	token := in.Login(t, "empty-allowlist", "empty-allowlist-password")

	resp, raw := in.Do(t, http.MethodGet, "/api/libraries/"+in.IDs["library"]+"/items", token, nil)
	var page struct {
		Results []json.RawMessage `json:"results"`
		Total   int               `json:"total"`
	}
	if err := json.Unmarshal(raw, &page); resp.StatusCode != http.StatusOK || err != nil {
		t.Fatalf("library items: %d %s", resp.StatusCode, raw)
	}
	if page.Total != 0 || len(page.Results) != 0 {
		t.Fatalf("library list returned %d items (total %d) that item reads deny", len(page.Results), page.Total)
	}
	if resp, raw := in.Do(t, http.MethodGet, "/api/items/"+in.IDs["novel"], token, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("direct item read: %d %s, want 403", resp.StatusCode, raw)
	}
}
