package users

import (
	"encoding/json"
	"reflect"
	"testing"

	"audiobookshelf-compat/internal/store"
)

func ptr[T any](v T) *T { return &v }

// User.toOldJSONForBrowser (server/models/User.js:596-640).
func TestUserDTOShape(t *testing.T) {
	user := store.User{
		ID: "u1", Username: "alice", Type: "user", Token: "tok", IsActive: true,
		Email:       ptr(""),
		Permissions: json.RawMessage(`{"download":true,"librariesAccessible":["l1"],"itemTagsSelected":["t"]}`),
		Bookmarks:   json.RawMessage(`[{"libraryItemId":"i1","time":3}]`),
		ExtraData:   json.RawMessage(`{"authOpenIDSub":"sub-1"}`),
	}
	dto := UserDTO(user, nil)
	if dto["email"] != "" {
		t.Errorf("a stored empty email stays \"\", got %#v", dto["email"])
	}
	if !reflect.DeepEqual(dto["librariesAccessible"], []string{"l1"}) || !reflect.DeepEqual(dto["itemTagsSelected"], []string{"t"}) {
		t.Errorf("accessible lists = %#v %#v", dto["librariesAccessible"], dto["itemTagsSelected"])
	}
	perms := dto["permissions"].(map[string]any)
	if _, ok := perms["librariesAccessible"]; ok || perms["download"] != true {
		t.Errorf("permissions = %#v: the lists move to the top level", perms)
	}
	if dto["hasOpenIDLink"] != true || dto["lastSeen"] != nil || dto["createdAt"] != nil {
		t.Errorf("dto = %#v", dto)
	}

	bare := UserDTO(store.User{ID: "u2"}, nil)
	if bare["email"] != nil {
		t.Errorf("NULL email must be null, got %#v", bare["email"])
	}
	if !reflect.DeepEqual(bare["librariesAccessible"], []string{}) || !reflect.DeepEqual(bare["itemTagsSelected"], []string{}) ||
		!reflect.DeepEqual(bare["seriesHideFromContinueListening"], []any{}) || bare["hasOpenIDLink"] != false {
		t.Errorf("empty user = %#v: lists must serialize as arrays", bare)
	}
}

func TestListedUserDTOHidesRootTokenAndHeavyFields(t *testing.T) {
	root := store.User{ID: "r", Type: "root", Token: "secret"}
	listed := ListedUserDTO(root, true)
	if listed["token"] != "" {
		t.Errorf("root token must be blank, got %#v", listed["token"])
	}
	if _, ok := listed["mediaProgress"]; ok {
		t.Error("listed users carry no progress")
	}
	if _, ok := listed["bookmarks"]; ok {
		t.Error("listed users carry no bookmarks")
	}
	if ListedUserDTO(root, false)["token"] != "secret" || ListedUserDTO(store.User{Type: "user", Token: "t"}, true)["token"] != "t" {
		t.Error("only the root token is hidden, and only when asked")
	}
}

func TestOnlineUserDTOZeroLastSeenIsNull(t *testing.T) {
	zero, seen := int64(0), int64(5)
	if dto := OnlineUserDTO(store.User{ID: "u", LastSeen: &zero}, nil); dto["lastSeen"] != nil {
		t.Errorf("lastSeen 0 is null (`lastSeen || null`), got %#v", dto["lastSeen"])
	}
	if dto := OnlineUserDTO(store.User{ID: "u", LastSeen: &seen}, "s"); dto["lastSeen"] != int64(5) || dto["session"] != "s" {
		t.Errorf("dto = %#v", dto)
	}
}

func TestNewUserExtraData(t *testing.T) {
	if got := NewUserExtraData(""); got != `{"seriesHideFromContinueListening":[]}` {
		t.Errorf("no sub = %s", got)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(NewUserExtraData("abc")), &doc); err != nil || doc["authOpenIDSub"] != "abc" {
		t.Errorf("sub = %v %v", doc, err)
	}
}
