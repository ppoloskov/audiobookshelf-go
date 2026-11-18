package user_misc

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"audiobookshelf-compat/testmachine/scenarios/harness"
)

const (
	keysListSource = "server/controllers/ApiKeyController.js:19-46"
	keysCreate     = "server/controllers/ApiKeyController.js:48-110"
	keysUpdate     = "server/controllers/ApiKeyController.js:112-177"
	keysDelete     = "server/controllers/ApiKeyController.js:179-193"
	keysMiddleware = "server/controllers/ApiKeyController.js:195-207"
	keysTable      = "client/components/tables/ApiKeysTable.vue:24-134"
	keysModal      = "client/components/modals/ApiKeyModal.vue:118-165"
)

const (
	adminName     = "misc-keys-admin"
	adminPassword = "misc-admin-password"
)

func apiKeyScenarios() []harness.Scenario {
	return []harness.Scenario{
		{
			Requirement: "route-r199", Invariant: "INV-R199-LIST-001", Case: "normal", State: "stateless", Assertion: "keys-list-empty",
			Sources: []string{"server/routers/ApiRouter.js:338", keysListSource, keysMiddleware},
			Claim:   "GET /api/api-keys as root returns {apiKeys: []} on the empty fixture table.",
			Run: func(t *testing.T, e *harness.Env) {
				if rows := e.Rows(t, "SELECT id FROM apiKeys"); len(rows) != 0 {
					t.Fatalf("fixture must have no api keys: %v", rows)
				}
				var body struct {
					APIKeys []apiKey `json:"apiKeys"`
				}
				getJSON(t, e, e.Token(t, "root"), "/api/api-keys", &body)
				if body.APIKeys == nil || len(body.APIKeys) != 0 {
					t.Fatalf("want an empty array: %+v", body.APIKeys)
				}
			},
		},
		{
			Requirement: "route-r199", Invariant: "INV-R199-LIST-001", Case: "normal", State: "stateless", Assertion: "keys-list-created-key",
			Sources: []string{"server/routers/ApiRouter.js:338", keysListSource, keysTable},
			Claim:   "GET /api/api-keys returns each key with its id, name, ISO expiry, null description/lastUsedAt/permissions, active flag, timestamps, userId and the user and createdByUser projections, and never the signed key secret or a password hash.",
			Run: func(t *testing.T, e *harness.Env) {
				created := createKey(t, e, map[string]any{"name": "misc-list-key", "userId": finisherID, "isActive": true, "expiresIn": 600})
				resp := e.Get(t, e.Token(t, "root"), "/api/api-keys")
				wantStatus(t, resp, http.StatusOK, "list keys")
				if strings.Contains(string(resp.Body), "pash") || strings.Contains(string(resp.Body), "$2a$") {
					t.Fatalf("key list leaks credential data: %s", resp.Body)
				}
				var body struct {
					APIKeys []apiKey `json:"apiKeys"`
				}
				resp.JSON(t, &body)
				if len(body.APIKeys) != 1 {
					t.Fatalf("want one key: %s", resp.Body)
				}
				key := body.APIKeys[0]
				if key.APIKey != "" {
					t.Fatalf("the secret must not be listed: %+v", key)
				}
				if key.ID != created.ID || key.Name != "misc-list-key" || key.UserID != finisherID || !key.IsActive {
					t.Fatalf("listed key fields wrong: %+v", key)
				}
				if key.Description != nil || key.LastUsedAt != nil || key.CreatedAt == "" || key.UpdatedAt == "" || key.ExpiresAt == nil {
					t.Fatalf("listed nullability/timestamps wrong: %+v", key)
				}
				parseTime(t, *key.ExpiresAt, "expiresAt")
				if key.User == nil || key.User.ID != finisherID || key.User.Username != "trial-finisher" || key.User.Type != "user" {
					t.Fatalf("user projection wrong: %+v", key.User)
				}
				if key.CreatedByUser == nil || key.CreatedByUser.ID != rootID || key.CreatedByUser.Username != "trial-root" || key.CreatedByUser.Type != "root" {
					t.Fatalf("createdByUser projection wrong: %+v", key.CreatedByUser)
				}
			},
		},
		{
			Requirement: "route-r199", Invariant: "INV-R199-LIST-001", Case: "edge", State: "stateless", Assertion: "keys-list-never-expiring",
			Sources: []string{keysListSource, keysTable, "server/models/ApiKey.js:48-80"},
			Claim:   "A key created without expiresIn is listed with expiresAt null and isActive false next to the expiring key, and both carry their user projection.",
			Run: func(t *testing.T, e *harness.Env) {
				never := createKey(t, e, map[string]any{"name": "misc-key-never", "userId": finisherID})
				if never.ExpiresAt != nil || never.IsActive {
					t.Fatalf("minimal key must never expire and be inactive: %+v", never)
				}
				keys := listKeys(t, e, e.Token(t, "root"))
				if len(keys) != 2 {
					t.Fatalf("want two keys: %+v", keys)
				}
				if got := findKey(t, keys, never.ID); got.ExpiresAt != nil || got.IsActive {
					t.Fatalf("listed minimal key wrong: %+v", got)
				}
				for _, key := range keys {
					if key.User == nil || key.User.ID != finisherID {
						t.Fatalf("key %s has no finisher projection: %+v", key.ID, key.User)
					}
				}
			},
		},
		{
			Requirement: "route-r199", Invariant: "INV-R199-SAMPLED", Case: "error", State: "stateless", Assertion: "keys-list-forbidden",
			Sources: []string{keysMiddleware, "server/routers/ApiRouter.js:338"},
			Claim:   "GET /api/api-keys answers 403 for a non-admin user and 401 without a valid token; an admin that is not root may list every key.",
			Run: func(t *testing.T, e *harness.Env) {
				wantStatus(t, e.Get(t, e.Token(t, "listener"), "/api/api-keys"), http.StatusForbidden, "non-admin list")
				requireUnauthenticated(t, e, http.MethodGet, "/api/api-keys")
				if keys := listKeys(t, e, adminToken(t, e)); len(keys) != 2 {
					t.Fatalf("a non-root admin must list both keys: %+v", keys)
				}
			},
		},
		{
			Requirement: "route-r200", Invariant: "INV-R200-SAMPLED", Case: "normal", State: "initial", Assertion: "key-create",
			Sources: []string{"server/routers/ApiRouter.js:339", keysCreate, keysModal},
			Claim:   "POST /api/api-keys as root creates the named active key for an existing user with expiresIn seconds, answers 200 with the signed key and its user projection, persists the row and authenticates as that user until the expiry.",
			Run: func(t *testing.T, e *harness.Env) {
				before := time.Now()
				key := createKey(t, e, map[string]any{"name": "misc-key-one", "userId": e.IDs["listener"], "isActive": true, "expiresIn": 3600})
				requireKeyFields(t, key, "misc-key-one", e.IDs["listener"], true)
				if key.ExpiresAt == nil {
					t.Fatal("an expiring key must have expiresAt")
				}
				expires := parseTime(t, *key.ExpiresAt, "expiresAt")
				created := parseTime(t, key.CreatedAt, "createdAt")
				if expires.Sub(created) < 3599*time.Second || expires.Sub(created) > 3601*time.Second {
					t.Fatalf("expiresAt must be 3600s after creation: %s", expires.Sub(created))
				}
				if created.Before(before.Add(-time.Minute)) || created.After(time.Now().Add(time.Minute)) {
					t.Fatalf("createdAt is not the request time: %s", created)
				}
				row := keyRows(t, e, key.ID)
				if len(row) != 1 || fmtString(row[0]["name"]) != "misc-key-one" || fmtString(row[0]["userId"]) != e.IDs["listener"] || fmt.Sprint(row[0]["isActive"]) != "1" {
					t.Fatalf("persisted key row wrong: %v", row)
				}
				requireKeyToken(t, e, key.APIKey, e.IDs["listener"])
			},
		},
		{
			Requirement: "route-r200", Invariant: "INV-R200-SAMPLED", Case: "normal", State: "repeat", Assertion: "key-create-repeat",
			Sources: []string{keysCreate},
			Claim:   "Creating a second key with the same name is allowed: it gets a different id, its own row, and both rows keep the same name and user.",
			Run: func(t *testing.T, e *harness.Env) {
				first := keyIDOf(t, e, "misc-key-one")
				second := createKey(t, e, map[string]any{"name": "misc-key-one", "userId": e.IDs["listener"], "isActive": true, "expiresIn": 3600})
				if second.ID == first {
					t.Fatal("the second key must get a new id")
				}
				if count := e.Count(t, "SELECT COUNT(*) FROM apiKeys WHERE name = 'misc-key-one'"); count != 2 {
					t.Fatalf("want two rows with the same name, got %d", count)
				}
				requireKeyToken(t, e, second.APIKey, e.IDs["listener"])
			},
		},
		{
			Requirement: "route-r200", Invariant: "INV-R200-SAMPLED", Case: "normal", State: "changed", Assertion: "key-create-target-user",
			Sources: []string{keysCreate, "server/auth/TokenManager.js:277-305"},
			Claim:   "A key created for the finisher authenticates its bearer as the finisher, not as the creating root.",
			Run: func(t *testing.T, e *harness.Env) {
				key := createKey(t, e, map[string]any{"name": "misc-key-finisher", "userId": finisherID, "isActive": true, "expiresIn": 600})
				requireKeyToken(t, e, key.APIKey, finisherID)
			},
		},
		{
			Requirement: "route-r200", Invariant: "INV-R200-SAMPLED", Case: "edge", State: "changed", Assertion: "key-create-minimal",
			Sources: []string{keysCreate},
			Claim:   "A create body without expiresIn and isActive stores a never-expiring inactive key (expiresAt null, isActive false) whose token is rejected.",
			Run: func(t *testing.T, e *harness.Env) {
				key := createKey(t, e, map[string]any{"name": "misc-key-min", "userId": finisherID})
				if key.ExpiresAt != nil || key.IsActive {
					t.Fatalf("minimal key wrong: %+v", key)
				}
				row := keyRows(t, e, key.ID)
				if len(row) != 1 || fmt.Sprint(row[0]["isActive"]) != "0" {
					t.Fatalf("persisted minimal key wrong: %v", row)
				}
				requireKeyRejected(t, e, key.APIKey, "inactive key")

				zero := createKey(t, e, map[string]any{"name": "misc-key-zero", "userId": finisherID, "expiresIn": 0})
				if zero.ExpiresAt != nil || zero.IsActive {
					t.Fatalf("expiresIn 0 must behave like no expiry: %+v", zero)
				}
				requireKeyRejected(t, e, zero.APIKey, "zero-expiry key")
			},
		},
		{
			Requirement: "route-r200", Invariant: "INV-R200-ERRORS-001", Case: "error", State: "failure", Assertion: "key-create-errors",
			Sources: []string{keysCreate, keysMiddleware},
			Claim:   "POST /api/api-keys answers 400 for a missing or non-string name, a non-positive or non-numeric expiresIn, a missing/unknown userId, 403 when a non-root admin targets root, and 401 without a token; every rejection adds no row.",
			Run: func(t *testing.T, e *harness.Env) {
				count := apiKeyCount(t, e)
				root := e.Token(t, "root")
				cases := []struct {
					body   map[string]any
					status int
				}{
					{map[string]any{"userId": finisherID}, http.StatusBadRequest},
					{map[string]any{"name": 42, "userId": finisherID}, http.StatusBadRequest},
					{map[string]any{"name": "x", "userId": finisherID, "expiresIn": -5}, http.StatusBadRequest},
					{map[string]any{"name": "x", "userId": finisherID, "expiresIn": "3600"}, http.StatusBadRequest},
					{map[string]any{"name": "x", "userId": finisherID, "expiresIn": true}, http.StatusBadRequest},
					{map[string]any{"name": "x"}, http.StatusBadRequest},
					{map[string]any{"name": "x", "userId": "00000000-0000-4000-8000-000000000000"}, http.StatusBadRequest},
					{map[string]any{"name": "x", "userId": 7}, http.StatusBadRequest},
				}
				for _, c := range cases {
					wantStatus(t, e.Post(t, root, "/api/api-keys", c.body), c.status, "invalid create")
					if got := apiKeyCount(t, e); got != count {
						t.Fatalf("rejected create added a row: %d -> %d", count, got)
					}
				}
				wantStatus(t, e.Post(t, adminToken(t, e), "/api/api-keys", map[string]any{"name": "root-key", "userId": rootID, "isActive": true}), http.StatusForbidden, "non-root admin targets root")
				requireUnauthenticated(t, e, http.MethodPost, "/api/api-keys")
				if got := apiKeyCount(t, e); got != count {
					t.Fatalf("rejected creates added rows: %d -> %d", count, got)
				}
			},
		},
		{
			Requirement: "route-r201", Invariant: "INV-R201-SAMPLED", Case: "normal", State: "initial", Assertion: "key-update-deactivate",
			Sources: []string{"server/routers/ApiRouter.js:340", keysUpdate},
			Claim:   "PATCH /api/api-keys/:id with isActive false answers 200 with the deactivated key, moves updatedAt forward, persists the flag and makes the key token unauthorized.",
			Run: func(t *testing.T, e *harness.Env) {
				key := createKey(t, e, map[string]any{"name": "misc-key-toggle", "userId": e.IDs["listener"], "isActive": true, "expiresIn": 600})
				resp := e.Patch(t, e.Token(t, "root"), "/api/api-keys/"+key.ID, map[string]any{"isActive": false})
				wantStatus(t, resp, http.StatusOK, "deactivate key")
				var body struct {
					APIKey apiKey `json:"apiKey"`
				}
				resp.JSON(t, &body)
				if body.APIKey.ID != key.ID || body.APIKey.IsActive || body.APIKey.Name != "misc-key-toggle" || body.APIKey.ExpiresAt == nil {
					t.Fatalf("patched key wrong: %+v", body.APIKey)
				}
				if !parseTime(t, body.APIKey.UpdatedAt, "updatedAt").After(parseTime(t, body.APIKey.CreatedAt, "createdAt")) {
					t.Fatalf("updatedAt must move forward: %+v", body.APIKey)
				}
				if body.APIKey.User == nil || body.APIKey.User.ID != e.IDs["listener"] {
					t.Fatalf("patched key user wrong: %+v", body.APIKey.User)
				}
				row := keyRows(t, e, key.ID)
				if len(row) != 1 || fmt.Sprint(row[0]["isActive"]) != "0" {
					t.Fatalf("persisted update wrong: %v", row)
				}
				requireKeyRejected(t, e, key.APIKey, "deactivated key")
			},
		},
		{
			Requirement: "route-r201", Invariant: "INV-R201-NOOP-001", Case: "normal", State: "repeat", Assertion: "key-update-noop",
			Sources: []string{keysUpdate},
			Claim:   "PATCH /api/api-keys/:id with the already stored values answers 200, keeps updatedAt and the persisted row unchanged and preserves the omitted name and expiry.",
			Run: func(t *testing.T, e *harness.Env) {
				id := keyIDOf(t, e, "misc-key-toggle")
				before := keyRows(t, e, id)
				var first apiKey
				for _, key := range listKeys(t, e, e.Token(t, "root")) {
					if key.ID == id {
						first = key
					}
				}
				resp := e.Patch(t, e.Token(t, "root"), "/api/api-keys/"+id, map[string]any{"isActive": false})
				wantStatus(t, resp, http.StatusOK, "no-op patch")
				var body struct {
					APIKey apiKey `json:"apiKey"`
				}
				resp.JSON(t, &body)
				if body.APIKey.UpdatedAt != first.UpdatedAt {
					t.Fatalf("no-op patch moved updatedAt: %s -> %s", first.UpdatedAt, body.APIKey.UpdatedAt)
				}
				if body.APIKey.Name != "misc-key-toggle" || body.APIKey.ExpiresAt == nil || first.ExpiresAt == nil || *body.APIKey.ExpiresAt != *first.ExpiresAt {
					t.Fatalf("omitted fields were not preserved: %+v", body.APIKey)
				}
				equalJSON(t, "row after no-op", keyRows(t, e, id), before)
			},
		},
		{
			Requirement: "route-r201", Invariant: "INV-R201-SAMPLED", Case: "edge", State: "changed", Assertion: "key-update-partial-reassign",
			Sources: []string{keysUpdate},
			Claim:   "PATCH /api/api-keys/:id with only userId reassigns the key to the finisher and keeps isActive false; a later patch with only isActive reactivates it and the token then authenticates as the finisher.",
			Run: func(t *testing.T, e *harness.Env) {
				token := e.Token(t, "root")
				key := createKey(t, e, map[string]any{"name": "misc-key-reassign", "userId": e.IDs["listener"], "isActive": false, "expiresIn": 600})
				resp := e.Patch(t, token, "/api/api-keys/"+key.ID, map[string]any{"userId": finisherID})
				wantStatus(t, resp, http.StatusOK, "reassign key")
				var body struct {
					APIKey apiKey `json:"apiKey"`
				}
				resp.JSON(t, &body)
				if body.APIKey.UserID != finisherID || body.APIKey.IsActive || body.APIKey.User == nil || body.APIKey.User.ID != finisherID {
					t.Fatalf("reassigned key wrong: %+v", body.APIKey)
				}
				requireKeyRejected(t, e, key.APIKey, "inactive reassigned key")

				reactivated := e.Patch(t, token, "/api/api-keys/"+key.ID, map[string]any{"isActive": true})
				wantStatus(t, reactivated, http.StatusOK, "reactivate key")
				var payload struct {
					APIKey apiKey `json:"apiKey"`
				}
				reactivated.JSON(t, &payload)
				if !payload.APIKey.IsActive || payload.APIKey.UserID != finisherID {
					t.Fatalf("reactivation wrong: %+v", payload.APIKey)
				}
				requireKeyToken(t, e, key.APIKey, finisherID)
			},
		},
		{
			Requirement: "route-r201", Invariant: "INV-R201-ERRORS-001", Case: "error", State: "failure", Assertion: "key-update-errors",
			Sources: []string{keysUpdate, keysMiddleware},
			Claim:   "PATCH /api/api-keys/:id answers 404 for an unknown id, 400 for a non-boolean isActive and a missing/unknown/typed userId, 403 for a non-admin and 403 when a non-root admin assigns a key to root, and none of these changes the stored key.",
			Run: func(t *testing.T, e *harness.Env) {
				id := keyIDOf(t, e, "misc-key-finisher")
				before := keyRows(t, e, id)
				root := e.Token(t, "root")
				wantStatus(t, e.Patch(t, root, "/api/api-keys/00000000-0000-4000-8000-000000000000", map[string]any{"isActive": true}), http.StatusNotFound, "unknown key")
				wantStatus(t, e.Patch(t, root, "/api/api-keys/00000000-0000-4000-8000-000000000000", map[string]any{"isActive": "not-a-boolean"}), http.StatusNotFound, "the key lookup runs before payload validation")
				for _, body := range []map[string]any{
					{"isActive": "true"},
					{"userId": 12},
					{"userId": "00000000-0000-4000-8000-000000000000"},
				} {
					wantStatus(t, e.Patch(t, root, "/api/api-keys/"+id, body), http.StatusBadRequest, "invalid patch")
					equalJSON(t, "row after rejected patch", keyRows(t, e, id), before)
				}
				wantStatus(t, e.Patch(t, e.Token(t, "listener"), "/api/api-keys/"+id, map[string]any{"isActive": false}), http.StatusForbidden, "non-admin patch")
				equalJSON(t, "row after forbidden patch", keyRows(t, e, id), before)
				wantStatus(t, e.Patch(t, adminToken(t, e), "/api/api-keys/"+id, map[string]any{"userId": rootID}), http.StatusForbidden, "assign a key to root as a non-root admin")
				equalJSON(t, "row after forbidden root assignment", keyRows(t, e, id), before)
				requireUnauthenticated(t, e, http.MethodPatch, "/api/api-keys/"+id)
			},
		},
		{
			Requirement: "route-r201", Invariant: "INV-R201-ERRORS-001", Case: "error", State: "failure", Assertion: "key-update-root-key-forbidden",
			Sources: []string{keysUpdate, keysMiddleware},
			Claim:   "A key owned by root can be created by root but a non-root admin cannot patch it: the answer is 403 and the stored key is unchanged.",
			Run: func(t *testing.T, e *harness.Env) {
				key := createKey(t, e, map[string]any{"name": "misc-key-root", "userId": rootID, "isActive": true, "expiresIn": 600})
				before := keyRows(t, e, key.ID)
				wantStatus(t, e.Patch(t, adminToken(t, e), "/api/api-keys/"+key.ID, map[string]any{"isActive": false}), http.StatusForbidden, "root key patch by admin")
				equalJSON(t, "root key row", keyRows(t, e, key.ID), before)
			},
		},
		{
			Requirement: "route-r202", Invariant: "INV-R202-SAMPLED", Case: "normal", State: "initial", Assertion: "key-delete-invalidates",
			Sources: []string{"server/routers/ApiRouter.js:341", keysDelete, keysTable},
			Claim:   "DELETE /api/api-keys/:id as root answers 200, removes only that row and makes its token unauthorized while the user and the other keys remain.",
			Run: func(t *testing.T, e *harness.Env) {
				key := createKey(t, e, map[string]any{"name": "misc-key-delete", "userId": e.IDs["listener"], "isActive": true, "expiresIn": 600})
				others := apiKeyCount(t, e)
				requireKeyToken(t, e, key.APIKey, e.IDs["listener"])
				wantStatus(t, e.Delete(t, e.Token(t, "root"), "/api/api-keys/"+key.ID), http.StatusOK, "delete key")
				if rows := keyRows(t, e, key.ID); len(rows) != 0 {
					t.Fatalf("deleted key row remains: %v", rows)
				}
				if count := apiKeyCount(t, e); count != others-1 {
					t.Fatalf("only one key may be removed: %d -> %d", others, count)
				}
				requireKeyRejected(t, e, key.APIKey, "deleted key")
				if rows := e.Rows(t, "SELECT id FROM users WHERE id = ?", e.IDs["listener"]); len(rows) != 1 {
					t.Fatal("deleting a key must keep its user")
				}
			},
		},
		{
			Requirement: "route-r202", Invariant: "INV-R202-SAMPLED", Case: "normal", State: "repeat", Assertion: "key-delete-repeat",
			Sources: []string{keysDelete},
			Claim:   "Deleting an already deleted key answers 404 and changes no other row.",
			Run: func(t *testing.T, e *harness.Env) {
				id := keyIDOf(t, e, "misc-key-finisher")
				root := e.Token(t, "root")
				wantStatus(t, e.Delete(t, root, "/api/api-keys/"+id), http.StatusOK, "first delete")
				count := apiKeyCount(t, e)
				wantStatus(t, e.Delete(t, root, "/api/api-keys/"+id), http.StatusNotFound, "repeat delete")
				if got := apiKeyCount(t, e); got != count {
					t.Fatalf("repeat delete changed the table: %d -> %d", count, got)
				}
			},
		},
		{
			Requirement: "route-r202", Invariant: "INV-R202-SAMPLED", Case: "edge", State: "changed", Assertion: "key-delete-keeps-others",
			Sources: []string{keysDelete, keysListSource},
			Claim:   "Deleting the remaining listener key keeps every other key and the user list intact; the returned list still carries the other users' keys.",
			Run: func(t *testing.T, e *harness.Env) {
				ids := make([]string, 0)
				for _, row := range e.Rows(t, "SELECT id FROM apiKeys ORDER BY id") {
					ids = append(ids, fmtString(row["id"]))
				}
				if len(ids) < 2 {
					t.Fatalf("want at least two keys: %v", ids)
				}
				wantStatus(t, e.Delete(t, e.Token(t, "root"), "/api/api-keys/"+ids[0]), http.StatusOK, "delete one key")
				keys := listKeys(t, e, adminToken(t, e))
				if len(keys) != len(ids)-1 {
					t.Fatalf("want %d keys after the delete, got %d", len(ids)-1, len(keys))
				}
				for _, key := range keys {
					if key.ID == ids[0] {
						t.Fatalf("deleted key still listed: %s", key.ID)
					}
					if key.User == nil || key.User.ID == "" {
						t.Fatalf("key %s lost its user projection", key.ID)
					}
				}
			},
		},
		{
			Requirement: "route-r202", Invariant: "INV-R202-ERRORS-001", Case: "error", State: "failure", Assertion: "key-delete-errors",
			Sources: []string{keysDelete, keysMiddleware},
			Claim:   "DELETE /api/api-keys/:id answers 403 for a non-admin and 404 for an unknown id, 401 without a token, and a forbidden delete removes nothing.",
			Run: func(t *testing.T, e *harness.Env) {
				keys := listKeys(t, e, e.Token(t, "root"))
				if len(keys) == 0 {
					t.Fatal("need a key to attempt the forbidden delete")
				}
				target := keys[0].ID
				count := apiKeyCount(t, e)
				wantStatus(t, e.Delete(t, e.Token(t, "listener"), "/api/api-keys/"+target), http.StatusForbidden, "non-admin delete")
				wantStatus(t, e.Delete(t, e.Token(t, "root"), "/api/api-keys/00000000-0000-4000-8000-000000000000"), http.StatusNotFound, "unknown key delete")
				if got := apiKeyCount(t, e); got != count {
					t.Fatalf("rejected deletes changed the table: %d -> %d", count, got)
				}
				requireUnauthenticated(t, e, http.MethodDelete, "/api/api-keys/"+target)
			},
		},
	}
}
