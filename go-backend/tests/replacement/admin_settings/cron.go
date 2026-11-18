package admin_settings

import (
	"net/http"
	"testing"

	"audiobookshelf-compat/tests/replacement/harness"
)

const cronSource = "server/controllers/MiscController.js:616-627"

func cronScenarios() []harness.Scenario {
	const req, inv = "route-r214", "INV-ROUTE-R214-ROUTE"
	postCron := func(t *testing.T, e *harness.Env, token string, body any) harness.Response {
		return e.Post(t, token, "/api/validate-cron", body)
	}
	return []harness.Scenario{
		scenario(req, inv, "normal", "initial", "validate-cron-valid-expressions",
			"POST /api/validate-cron answers 200 for valid cron expressions: the shapes the client's cron builder emits (intervals, daily, weekday lists) and a 6-field form.",
			[]string{cronSource},
			func(t *testing.T, e *harness.Env) {
				for _, expression := range []string{"* * * * *", "0 * * * *", "0 2 * * *", "0 0 1 1 *", "*/5 * * * * *",
					"0 */12 * * *", "0 */6 * * *", "0 */2 * * *", "*/30 * * * *", "*/15 * * * *", "30 8 * * 1,3,5", "0 0 * * 0,6", "5 23 * * 2", "15 4 * * *"} {
					wantStatus(t, "expression "+expression, postCron(t, e, e.Token(t, "root"), map[string]any{"expression": expression}), http.StatusOK)
				}
			}),
		scenario(req, inv, "error", "initial", "validate-cron-invalid-rejected",
			"POST /api/validate-cron answers 400 for a missing, empty, null or malformed expression and 401 without a token.",
			[]string{cronSource, "server/Auth.js:322"},
			func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				bodies := []any{
					map[string]any{}, map[string]any{"expression": ""}, map[string]any{"expression": nil},
					map[string]any{"expression": "not a cron"}, map[string]any{"expression": "61 * * * *"},
					map[string]any{"expression": "* * * *"}, map[string]any{"expression": "0 0 * * 8"},
				}
				for _, body := range bodies {
					wantStatus(t, "invalid body", postCron(t, e, root, body), http.StatusBadRequest)
				}
				wantStatus(t, "no token", postCron(t, e, "", map[string]any{"expression": "* * * * *"}), http.StatusUnauthorized)
			}),
		scenario(req, "INV-ROUTE-R214-REPEAT", "edge", "repeat", "validate-cron-repeat-same-answer",
			"The same valid and the same invalid expression get the same answer every time.",
			[]string{cronSource},
			func(t *testing.T, e *harness.Env) {
				root := e.Token(t, "root")
				for range 3 {
					wantStatus(t, "valid", postCron(t, e, root, map[string]any{"expression": "30 8 * * 1,3,5"}), http.StatusOK)
					wantStatus(t, "invalid", postCron(t, e, root, map[string]any{"expression": "nope"}), http.StatusBadRequest)
				}
			}),
		scenario(req, "INV-ROUTE-R214-REPEAT", "error", "failure", "validate-cron-never-persists",
			"Valid and invalid validation requests leave the settings row unchanged.",
			[]string{cronSource},
			func(t *testing.T, e *harness.Env) {
				before := storedSettings(t, e)
				root := e.Token(t, "root")
				postCron(t, e, root, map[string]any{"expression": "5 4 * * *"})
				postCron(t, e, root, map[string]any{"expression": "bad"})
				if !sameJSON(before, storedSettings(t, e)) {
					t.Fatal("validate-cron changed the settings row")
				}
			}),
	}
}
