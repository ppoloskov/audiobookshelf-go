package oracle

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	maxFlowFileBytes    = 16 << 20
	defaultSettleMillis = 250
)

// Flow describes a declarative differential test cassette executed against TS and Go.
type Flow struct {
	Name  string            `json:"name"`
	Fresh bool              `json:"fresh,omitempty"`
	Env   map[string]string `json:"env,omitempty"`
	Steps []Step            `json:"steps"`
}

// FileSpec describes one uploaded file part in a multipart step.
type FileSpec struct {
	Field         string `json:"field"`
	Name          string `json:"name"`
	ContentBase64 string `json:"content_base64,omitempty"`
	ContentText   string `json:"content_text,omitempty"`
}

// Step describes a single action and its differential assertions inside a Flow.
type Step struct {
	Name           string            `json:"name"`
	Kind           string            `json:"kind,omitempty"`
	Method         string            `json:"method,omitempty"`
	Path           string            `json:"path,omitempty"`
	Raw            bool              `json:"raw,omitempty"`
	Role           string            `json:"role,omitempty"`
	Header         map[string]string `json:"header,omitempty"`
	Query          map[string]string `json:"query,omitempty"`
	Body           json.RawMessage   `json:"body,omitempty"`
	Fields         map[string]string `json:"fields,omitempty"`
	Files          []FileSpec        `json:"files,omitempty"`
	SocketEvent    string            `json:"socket_event,omitempty"`
	SocketArgs     []json.RawMessage `json:"socket_args,omitempty"`
	FSPath         string            `json:"fs_path,omitempty"`
	FSContent      string            `json:"fs_content,omitempty"`
	SQL            string            `json:"sql,omitempty"`
	StubStatus     int               `json:"stub_status,omitempty"`
	StubType       string            `json:"stub_content_type,omitempty"`
	StubBody       string            `json:"stub_body,omitempty"`
	StubBodyBase64 string            `json:"stub_body_base64,omitempty"`
	Capture        map[string]string `json:"capture,omitempty"`
	CheckDB        bool              `json:"check_db,omitempty"`
	CheckFS        bool              `json:"check_fs,omitempty"`
	CheckSockets   bool              `json:"check_sockets,omitempty"`
	CheckStubs     bool              `json:"check_stubs,omitempty"`
	SettleMs       int               `json:"settle_ms,omitempty"`
	ExpectStatus   int               `json:"expect_status,omitempty"`
}

// LoadFlow reads, size-checks, and validates a declarative Flow JSON file.
func LoadFlow(filePath string) (Flow, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return Flow{}, fmt.Errorf("stat flow %s: %w", filePath, err)
	}
	if info.Size() > maxFlowFileBytes {
		return Flow{}, fmt.Errorf("flow file %s exceeds %d bytes", filePath, maxFlowFileBytes)
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return Flow{}, fmt.Errorf("read flow %s: %w", filePath, err)
	}
	var flow Flow
	if err := json.Unmarshal(data, &flow); err != nil {
		return Flow{}, fmt.Errorf("decode flow %s: %w", filePath, err)
	}
	if strings.TrimSpace(flow.Name) == "" {
		return Flow{}, fmt.Errorf("flow %s: name is required", filePath)
	}
	if len(flow.Steps) == 0 {
		return Flow{}, fmt.Errorf("flow %s: steps must not be empty", filePath)
	}
	return flow, nil
}

// RunFlowFile loads a flow JSON file, boots a Twin server pair, and runs the flow.
func RunFlowFile(t *testing.T, filePath string) {
	t.Helper()
	flow, err := LoadFlow(filePath)
	if err != nil {
		t.Fatalf("load flow: %v", err)
	}
	tw := StartTwin(t, Options{Fresh: flow.Fresh, Env: flow.Env})
	if tw.Stubs == nil {
		tw.Stubs = StartStubs(t)
	}
	RunFlow(t, tw, flow)
}

// RunFlow executes all steps in f against tw, comparing HTTP, SQLite, FS,
// Socket.IO, and outbound stub surfaces and failing on unexpected divergences.
func RunFlow(t *testing.T, tw *Twin, f Flow) {
	t.Helper()
	tsVars := initTargetVars(tw.TS, tw.Stubs, true)
	goVars := initTargetVars(tw.Go, tw.Stubs, false)

	for idx, step := range f.Steps {
		stepName := step.Name
		if stepName == "" {
			stepName = fmt.Sprintf("step-%03d", idx+1)
			step.Name = stepName
		}
		t.Run(stepName, func(t *testing.T) {
			runFlowStep(t, tw, step, tsVars, goVars)
		})
	}
}

func runFlowStep(t *testing.T, tw *Twin, step Step, tsVars, goVars map[string]string) {
	t.Helper()
	kind := step.Kind
	if kind == "" {
		kind = "http"
	}

	var tsMark, goMark map[string]int
	if step.CheckSockets && tw.WatchTS != nil && tw.WatchGo != nil {
		tsMark = tw.WatchTS.Mark()
		goMark = tw.WatchGo.Mark()
	}
	if step.CheckStubs && tw.Stubs != nil {
		tw.Stubs.Reset()
	}

	var findings []Finding
	switch kind {
	case "http", "multipart":
		tsReq := buildStepRequest(t, tw.TS, step, tsVars, kind == "multipart")
		goReq := buildStepRequest(t, tw.Go, step, goVars, kind == "multipart")
		tsResp := tw.TS.Do(t, tsReq)
		goResp := tw.Go.Do(t, goReq)
		if step.ExpectStatus != 0 && tsResp.Status != step.ExpectStatus {
			t.Errorf("step %s: TS status %d != expect_status %d (body: %s)", step.Name, tsResp.Status, step.ExpectStatus, excerpt(tsResp.Body))
		}
		findings = append(findings, tw.Diff.DiffHTTP(step.Name, tsResp, goResp)...)
		captureStepVars(t, tw.Diff.Corr, step.Capture, tsResp.Body, goResp.Body, tsVars, goVars)
	case "socket_emit":
		if step.Role == "" {
			t.Fatalf("step %s: socket_emit requires role", step.Name)
		}
		if tw.WatchTS == nil || tw.WatchGo == nil {
			t.Fatalf("step %s: socket_emit requires active WatchTS and WatchGo audiences", step.Name)
		}
		tsArgs := decodeSocketArgs(t, step.SocketArgs, tsVars)
		goArgs := decodeSocketArgs(t, step.SocketArgs, goVars)
		tw.WatchTS.Socket(step.Role).Emit(t, step.SocketEvent, tsArgs...)
		tw.WatchGo.Socket(step.Role).Emit(t, step.SocketEvent, goArgs...)
	case "fs_write":
		writeStepFile(t, tw.TS.Install, substVars(step.FSPath, tsVars), substVars(step.FSContent, tsVars))
		writeStepFile(t, tw.Go.Install, substVars(step.FSPath, goVars), substVars(step.FSContent, goVars))
	case "fs_delete":
		deleteStepPath(t, tw.TS.Install, substVars(step.FSPath, tsVars))
		deleteStepPath(t, tw.Go.Install, substVars(step.FSPath, goVars))
	case "db_exec":
		tw.TS.Stop(t)
		tw.Go.Stop(t)
		tw.TS.Exec(t, substVars(step.SQL, tsVars))
		tw.Go.Exec(t, substVars(step.SQL, goVars))
		tw.TS.Restart(t)
		tw.Go.Restart(t)
	case "restart":
		tw.TS.Restart(t)
		tw.Go.Restart(t)
	case "rollback_go_to_ts":
		rb := tw.RollbackGoToTS(t)
		verifyRollbackSmoke(t, rb, "rollback_go_to_ts")
	case "rollback_ts_to_go":
		rb := tw.RollbackTSToGo(t)
		verifyRollbackSmoke(t, rb, "rollback_ts_to_go")
	case "stub_route":
		if tw.Stubs == nil {
			t.Fatalf("step %s: stub_route requires active Stubs", step.Name)
		}
		var body []byte
		if step.StubBodyBase64 != "" {
			decoded, err := base64.StdEncoding.DecodeString(step.StubBodyBase64)
			if err != nil {
				t.Fatalf("step %s: decode stub_body_base64: %v", step.Name, err)
			}
			body = decoded
		} else {
			body = []byte(substVars(step.StubBody, tsVars))
		}
		tw.Stubs.SetRoute(substVars(step.Path, tsVars), step.StubStatus, step.StubType, body)
	default:
		t.Fatalf("step %s: unknown kind %q", step.Name, step.Kind)
	}

	if step.CheckSockets && tw.WatchTS != nil && tw.WatchGo != nil {
		settle := time.Duration(step.SettleMs) * time.Millisecond
		if settle <= 0 {
			settle = defaultSettleMillis * time.Millisecond
		}
		tsEvents := tw.WatchTS.Since(tsMark, settle)
		goEvents := tw.WatchGo.Since(goMark, settle)
		findings = append(findings, tw.Diff.DiffSockets(step.Name, tsEvents, goEvents)...)
	}
	if step.CheckStubs && tw.Stubs != nil {
		findings = append(findings, tw.Diff.DiffStubs(step.Name, tw.Stubs.Calls("ts"), tw.Stubs.Calls("go"))...)
	}
	if step.CheckDB {
		dbFindings, err := tw.Diff.DiffSQLite(step.Name, filepath.Join(tw.TS.Config, "absdatabase.sqlite"), filepath.Join(tw.Go.Config, "absdatabase.sqlite"))
		if err != nil {
			t.Errorf("step %s: diff sqlite: %v", step.Name, err)
		}
		findings = append(findings, dbFindings...)
	}
	if step.CheckFS {
		fsFindings, err := tw.Diff.DiffFS(step.Name, tw.TS.Install, tw.Go.Install)
		if err != nil {
			t.Errorf("step %s: diff fs: %v", step.Name, err)
		}
		findings = append(findings, fsFindings...)
	}

	unexpected, acknowledged := tw.Diff.Filter(findings)
	if len(acknowledged) > 0 {
		t.Logf("acknowledged divergences (%d):\n%s", len(acknowledged), FormatFindings(acknowledged))
	}
	if len(unexpected) > 0 {
		t.Errorf("unexpected divergences (%d):\n%s", len(unexpected), FormatFindings(unexpected))
	}
}

func initTargetVars(tgt *Target, stubs *StubSet, isTS bool) map[string]string {
	vars := make(map[string]string, len(tgt.IDs)+8)
	for k, v := range tgt.IDs {
		vars[k] = v
	}
	vars["install"] = tgt.Install
	vars["config"] = tgt.Config
	vars["metadata"] = tgt.Metadata
	vars["media"] = tgt.Media
	if stubs != nil {
		vars["stub_http_shared"] = stubs.HTTPURL
		if isTS {
			vars["stub_http"] = stubs.TSHTTPURL
			vars["stub_smtp"] = stubs.TSSMTPAddr
			if host, port, ok := strings.Cut(stubs.TSSMTPAddr, ":"); ok {
				vars["stub_smtp_host"] = host
				vars["stub_smtp_port"] = port
			}
		} else {
			vars["stub_http"] = stubs.GoHTTPURL
			vars["stub_smtp"] = stubs.GoSMTPAddr
			if host, port, ok := strings.Cut(stubs.GoSMTPAddr, ":"); ok {
				vars["stub_smtp_host"] = host
				vars["stub_smtp_port"] = port
			}
		}
	}
	return vars
}

func buildStepRequest(t *testing.T, tgt *Target, step Step, vars map[string]string, isMultipart bool) Request {
	t.Helper()
	req := Request{
		Method: step.Method,
		Path:   substVars(step.Path, vars),
		Raw:    step.Raw,
		Token:  resolveRoleToken(t, tgt, step.Role, vars),
	}
	if len(step.Header) > 0 {
		req.Header = make(map[string]string, len(step.Header))
		for k, v := range step.Header {
			req.Header[k] = substVars(v, vars)
		}
	}
	if len(step.Query) > 0 {
		req.Query = make(url.Values, len(step.Query))
		for k, v := range step.Query {
			req.Query.Set(k, substVars(v, vars))
		}
	}
	if isMultipart {
		fields := make(map[string]string, len(step.Fields))
		for k, v := range step.Fields {
			fields[k] = substVars(v, vars)
		}
		files := make([]File, 0, len(step.Files))
		for _, fs := range step.Files {
			content := []byte(substVars(fs.ContentText, vars))
			if fs.ContentBase64 != "" {
				decoded, err := base64.StdEncoding.DecodeString(fs.ContentBase64)
				if err != nil {
					t.Fatalf("step %s: decode base64 file %q: %v", step.Name, fs.Name, err)
				}
				content = decoded
			}
			files = append(files, File{Field: fs.Field, Name: fs.Name, Content: content})
		}
		body, ct := Multipart(fields, files...)
		req.Body = body
		if req.Header == nil {
			req.Header = make(map[string]string, 1)
		}
		req.Header["Content-Type"] = ct
		return req
	}
	if len(step.Body) > 0 {
		subBody := substVars(string(step.Body), vars)
		req.Body = []byte(subBody)
		if !hasHeader(req.Header, "Content-Type") {
			if req.Header == nil {
				req.Header = make(map[string]string, 1)
			}
			req.Header["Content-Type"] = "application/json"
		}
	}
	return req
}

func resolveRoleToken(t *testing.T, tgt *Target, role string, vars map[string]string) string {
	t.Helper()
	if role == "" {
		return ""
	}
	if strings.Contains(role, "{{") {
		return substVars(role, vars)
	}
	return tgt.Token(t, role)
}

func hasHeader(headers map[string]string, target string) bool {
	for k := range headers {
		if strings.EqualFold(k, target) {
			return true
		}
	}
	return false
}

func substVars(input string, vars map[string]string) string {
	if !strings.Contains(input, "{{") {
		return input
	}
	out := input
	for k, v := range vars {
		placeholder := "{{" + k + "}}"
		if strings.Contains(out, placeholder) {
			out = strings.ReplaceAll(out, placeholder, v)
		}
	}
	return out
}

func captureStepVars(t *testing.T, corr *Correspondence, capture map[string]string, tsBody, goBody []byte, tsVars, goVars map[string]string) {
	t.Helper()
	if len(capture) == 0 {
		return
	}
	var tsDoc, goDoc any
	if err := json.Unmarshal(tsBody, &tsDoc); err != nil {
		t.Logf("capture skipped: TS body not JSON: %v", err)
		return
	}
	if err := json.Unmarshal(goBody, &goDoc); err != nil {
		t.Logf("capture skipped: Go body not JSON: %v", err)
		return
	}
	for varName, jsonPath := range capture {
		tsVal, okTS := extractJSONPath(tsDoc, jsonPath)
		goVal, okGo := extractJSONPath(goDoc, jsonPath)
		if okTS {
			tsVars[varName] = tsVal
		}
		if okGo {
			goVars[varName] = goVal
		}
		if okTS && okGo && corr != nil && isUUID(tsVal) && isUUID(goVal) {
			corr.BindID(tsVal, goVal)
		}
	}
}

func extractJSONPath(doc any, dotPath string) (string, bool) {
	if dotPath == "" {
		return "", false
	}
	cur := doc
	for _, seg := range strings.Split(dotPath, ".") {
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[seg]
			if !ok {
				return "", false
			}
			cur = next
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil || idx < 0 || idx >= len(node) {
				return "", false
			}
			cur = node[idx]
		default:
			return "", false
		}
	}
	switch v := cur.(type) {
	case string:
		return v, true
	case float64:
		if math.Trunc(v) == v && math.Abs(v) <= 1e15 {
			return strconv.FormatInt(int64(v), 10), true
		}
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(v), true
	default:
		return "", false
	}
}

func decodeSocketArgs(t *testing.T, rawArgs []json.RawMessage, vars map[string]string) []any {
	t.Helper()
	args := make([]any, 0, len(rawArgs))
	for _, raw := range rawArgs {
		sub := substVars(string(raw), vars)
		var decoded any
		if err := json.Unmarshal([]byte(sub), &decoded); err != nil {
			t.Fatalf("decode socket arg %q: %v", sub, err)
		}
		args = append(args, decoded)
	}
	return args
}

func resolveSafeInstallPath(installRoot, relPath string) (string, error) {
	if relPath == "" || filepath.IsAbs(relPath) {
		return "", fmt.Errorf("fs_path %q must be a non-empty relative path", relPath)
	}
	cleaned := filepath.Clean(relPath)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("fs_path %q escapes install root", relPath)
	}
	return filepath.Join(installRoot, cleaned), nil
}

func writeStepFile(t *testing.T, installRoot, relPath, content string) {
	t.Helper()
	target, err := resolveSafeInstallPath(installRoot, relPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(target), err)
	}
	if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", target, err)
	}
}

func deleteStepPath(t *testing.T, installRoot, relPath string) {
	t.Helper()
	target, err := resolveSafeInstallPath(installRoot, relPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(target); err != nil {
		t.Fatalf("remove %s: %v", target, err)
	}
}

func verifyRollbackSmoke(t *testing.T, rb *Target, label string) {
	t.Helper()
	tok := rb.Token(t, "root")
	for _, p := range []string{"/api/me", "/api/libraries"} {
		resp := rb.Do(t, Request{Path: p, Token: tok})
		if resp.Status != 200 {
			t.Errorf("%s: GET %s returned %d (expected 200): %s", label, p, resp.Status, excerpt(resp.Body))
		}
	}
}
