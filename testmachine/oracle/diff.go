package oracle

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

// Finding describes a single detected divergence between TypeScript and Go.
type Finding struct {
	Step    string
	Surface string
	Path    string
	TSValue string
	GoValue string
	Detail  string
}

// Correspondence tracks bijective IDs, filesystem inodes, stub endpoints,
// and install paths across TypeScript and Go servers so equivalent entities
// normalize identically.
type Correspondence struct {
	mu          sync.RWMutex
	tsInstall   string
	goInstall   string
	tsServerURL string
	goServerURL string
	tsHTTPURL   string
	goHTTPURL   string
	tsSMTPAddr  string
	goSMTPAddr  string
	tsSMTPPort  int64
	goSMTPPort  int64
	tsToGo      map[string]string
	goToTS      map[string]string
	tsCanonical map[string]string
	goCanonical map[string]string
	tsInodes    map[string]string
	goInodes    map[string]string
	nextID      int
}

// NewCorrespondence creates a Correspondence configured with both install roots.
func NewCorrespondence(tsInstall, goInstall string) *Correspondence {
	return &Correspondence{
		tsInstall:   strings.TrimRight(filepath.ToSlash(tsInstall), "/"),
		goInstall:   strings.TrimRight(filepath.ToSlash(goInstall), "/"),
		tsToGo:      make(map[string]string),
		goToTS:      make(map[string]string),
		tsCanonical: make(map[string]string),
		goCanonical: make(map[string]string),
		tsInodes:    make(map[string]string),
		goInodes:    make(map[string]string),
	}
}

// RegisterServerURLs records the TypeScript and Go server base URLs so
// absolute self-links (such as RSS feed URLs) normalize identically.
func (c *Correspondence) RegisterServerURLs(tsURL, goURL string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tsServerURL = strings.TrimRight(tsURL, "/")
	c.goServerURL = strings.TrimRight(goURL, "/")
}

// RegisterStubs records the per-side stub HTTP URLs and SMTP addresses/ports
// so outbound endpoint references normalize identically across TypeScript and Go.
func (c *Correspondence) RegisterStubs(tsHTTP, goHTTP, tsSMTP, goSMTP string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tsHTTPURL = strings.TrimRight(tsHTTP, "/")
	c.goHTTPURL = strings.TrimRight(goHTTP, "/")
	c.tsSMTPAddr = strings.TrimSpace(tsSMTP)
	c.goSMTPAddr = strings.TrimSpace(goSMTP)
	c.tsSMTPPort = parsePort(tsSMTP)
	c.goSMTPPort = parsePort(goSMTP)
}

// MatchStubSMTPPort reports whether tsPort and goPort are positive and match
// the registered TypeScript and Go SMTP stub ports respectively.
func (c *Correspondence) MatchStubSMTPPort(tsPort, goPort int64) bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	tp, gp := c.tsSMTPPort, c.goSMTPPort
	c.mu.RUnlock()
	return tp > 0 && gp > 0 && tsPort == tp && goPort == gp
}

// HasInode reports whether ino is registered as a known fixture inode for side ("ts" or "go").
func (c *Correspondence) HasInode(side, ino string) bool {
	if c == nil || ino == "" {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if side == "go" {
		_, ok := c.goInodes[ino]
		return ok
	}
	_, ok := c.tsInodes[ino]
	return ok
}

func parsePort(addr string) int64 {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return 0
	}
	if _, portStr, err := net.SplitHostPort(addr); err == nil {
		addr = portStr
	}
	port, err := strconv.ParseInt(addr, 10, 64)
	if err != nil || port <= 0 {
		return 0
	}
	return port
}

// RegisterInodes walks each media and metadata root pair and maps file inodes
// to "<inode:rel>" placeholders independently for TypeScript and Go.
func (c *Correspondence) RegisterInodes(tsMedia, goMedia, tsMeta, goMeta string) {
	tsMap := make(map[string]string)
	goMap := make(map[string]string)
	walkInodes(tsMedia, tsMap)
	walkInodes(tsMeta, tsMap)
	walkInodes(goMedia, goMap)
	walkInodes(goMeta, goMap)

	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range tsMap {
		c.tsInodes[k] = v
	}
	for k, v := range goMap {
		c.goInodes[k] = v
	}
}

func walkInodes(root string, dst map[string]string) {
	if root == "" {
		return
	}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // missing or unreadable dir is skipped during best-effort registration
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil // skip disappearing entries
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || st.Ino == 0 {
			return nil
		}
		dst[strconv.FormatUint(uint64(st.Ino), 10)] = "<inode:" + filepath.ToSlash(rel) + ">"
		return nil
	})
}

// BindID records an explicit bijection pair between tsID and goID.
func (c *Correspondence) BindID(tsID, goID string) {
	if tsID == "" || goID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	canon, ok := c.tsCanonical[tsID]
	if !ok {
		canon, ok = c.goCanonical[goID]
	}
	if !ok {
		c.nextID++
		canon = fmt.Sprintf("<id:%d>", c.nextID)
	}
	c.tsToGo[tsID] = goID
	c.goToTS[goID] = tsID
	c.tsCanonical[tsID] = canon
	c.goCanonical[goID] = canon
}

// MapForGo returns the Go ID bound to tsID, if any.
func (c *Correspondence) MapForGo(tsID string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if v, ok := c.tsToGo[tsID]; ok {
		return v, true
	}
	v, ok := c.tsToGo[strings.ToLower(tsID)]
	return v, ok
}

// MapForTS returns the TypeScript ID bound to goID, if any.
func (c *Correspondence) MapForTS(goID string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if v, ok := c.goToTS[goID]; ok {
		return v, true
	}
	v, ok := c.goToTS[strings.ToLower(goID)]
	return v, ok
}

// CanonicalID returns "<id:N>" if id is mapped on the given side ("ts" or "go"),
// or id unchanged otherwise.
func (c *Correspondence) CanonicalID(side, id string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m := c.canonicalMapLocked(side)
	if canon, ok := m[id]; ok {
		return canon
	}
	if canon, ok := m[strings.ToLower(id)]; ok {
		return canon
	}
	return id
}

// BindPair binds two 36-char hex-hyphen UUIDs bijectively if neither conflicts
// with an existing binding, returning false on non-UUIDs or binding conflicts.
func (c *Correspondence) BindPair(tsID, goID string) bool {
	if !isUUID(tsID) || !isUUID(goID) {
		return false
	}
	tsKey := strings.ToLower(tsID)
	goKey := strings.ToLower(goID)

	c.mu.Lock()
	defer c.mu.Unlock()
	if existingGo, ok := c.tsToGo[tsKey]; ok && existingGo != goKey {
		return false
	}
	if existingTS, ok := c.goToTS[goKey]; ok && existingTS != tsKey {
		return false
	}
	if _, ok := c.tsToGo[tsKey]; !ok {
		c.nextID++
		canon := fmt.Sprintf("<id:%d>", c.nextID)
		c.tsToGo[tsKey] = goKey
		c.goToTS[goKey] = tsKey
		c.tsCanonical[tsKey] = canon
		c.goCanonical[goKey] = canon
	}
	return true
}

// NormalizeString replaces the install root, stub endpoints, registered inodes,
// and bound IDs inside s for the given side ("ts" or "go").
func (c *Correspondence) NormalizeString(side, s string) string {
	if s == "" || c == nil {
		return s
	}
	c.mu.RLock()
	install := c.tsInstall
	serverURL := c.tsServerURL
	httpURL := c.tsHTTPURL
	smtpAddr := c.tsSMTPAddr
	inodes := c.tsInodes
	if side == "go" {
		install = c.goInstall
		serverURL = c.goServerURL
		httpURL = c.goHTTPURL
		smtpAddr = c.goSMTPAddr
		inodes = c.goInodes
	}
	canonMap := c.canonicalMapLocked(side)

	idPairs := make([]kvPair, 0, len(canonMap))
	for k, v := range canonMap {
		idPairs = append(idPairs, kvPair{k: k, v: v})
	}
	inoPairs := make([]kvPair, 0, len(inodes))
	for k, v := range inodes {
		inoPairs = append(inoPairs, kvPair{k: k, v: v})
	}
	c.mu.RUnlock()

	if install != "" {
		s = strings.ReplaceAll(s, install, "<install>")
	}
	if httpURL != "" {
		s = strings.ReplaceAll(s, httpURL, "<stub_http>")
	}
	if serverURL != "" {
		s = strings.ReplaceAll(s, serverURL, "<server_url>")
	}
	if smtpAddr != "" {
		s = strings.ReplaceAll(s, smtpAddr, "<stub_smtp>")
	}
	sortByKeyLenDesc(idPairs)
	for _, p := range idPairs {
		if len(p.k) >= 8 {
			s = replaceFolded(s, p.k, p.v)
		} else if s == p.k {
			s = p.v
		}
	}
	sortByKeyLenDesc(inoPairs)
	for _, p := range inoPairs {
		s = replaceBoundedDigits(s, p.k, p.v)
	}
	return s
}

func (c *Correspondence) canonicalMapLocked(side string) map[string]string {
	if side == "go" {
		return c.goCanonical
	}
	return c.tsCanonical
}

type kvPair struct {
	k string
	v string
}

func sortByKeyLenDesc(pairs []kvPair) {
	sort.Slice(pairs, func(i, j int) bool {
		if len(pairs[i].k) != len(pairs[j].k) {
			return len(pairs[i].k) > len(pairs[j].k)
		}
		return pairs[i].k < pairs[j].k
	})
}

func replaceFolded(s, target, repl string) string {
	if target == "" {
		return s
	}
	if strings.Contains(s, target) {
		s = strings.ReplaceAll(s, target, repl)
	}
	lowerTarget := strings.ToLower(target)
	if lowerTarget == target {
		return s
	}
	return strings.ReplaceAll(s, lowerTarget, repl)
}

func replaceBoundedDigits(s, digits, repl string) string {
	if digits == "" || !strings.Contains(s, digits) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		idx := strings.Index(s[i:], digits)
		if idx < 0 {
			b.WriteString(s[i:])
			break
		}
		abs := i + idx
		end := abs + len(digits)
		leftDigit := abs > 0 && isASCIIDigit(s[abs-1])
		rightDigit := end < len(s) && isASCIIDigit(s[end])
		if !leftDigit && !rightDigit {
			b.WriteString(s[i:abs])
			b.WriteString(repl)
			i = end
			continue
		}
		b.WriteString(s[i : abs+1])
		i = abs + 1
	}
	return b.String()
}

func isASCIIDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

func isBcryptHash(s string) bool {
	return len(s) == 60 && (strings.HasPrefix(s, "$2a$") || strings.HasPrefix(s, "$2b$") || strings.HasPrefix(s, "$2y$"))
}

func isNonJSONErrorContentType(ct string) bool {
	lower := strings.ToLower(strings.TrimSpace(ct))
	return lower == "" || strings.HasPrefix(lower, "text/plain") || strings.HasPrefix(lower, "text/html")
}

// Differ runs semantic differential comparisons across HTTP, SQLite, FS,
// Socket.IO, and outbound stub calls.
type Differ struct {
	Corr   *Correspondence
	Ledger *DivergenceLedger
}

// NewDiffer constructs a Differ with correspondence state and an optional ledger.
func NewDiffer(tsInstall, goInstall string, ledger *DivergenceLedger) *Differ {
	if ledger == nil {
		ledger = &DivergenceLedger{}
	}
	return &Differ{
		Corr:   NewCorrespondence(tsInstall, goInstall),
		Ledger: ledger,
	}
}

// Filter partitions findings into unexpected findings and ledger-acknowledged ones.
func (d *Differ) Filter(findings []Finding) (unexpected []Finding, acknowledged []Finding) {
	for _, f := range findings {
		if d.Ledger != nil {
			if _, ok := d.Ledger.Match(f); ok {
				acknowledged = append(acknowledged, f)
				continue
			}
		}
		unexpected = append(unexpected, f)
	}
	return unexpected, acknowledged
}

// DiffHTTP compares HTTP status, contract headers, cookies, and response bodies.
func (d *Differ) DiffHTTP(step string, tsResp, goResp Response) []Finding {
	var findings []Finding
	if tsResp.Status != goResp.Status {
		findings = append(findings, Finding{
			Step:    step,
			Surface: "http",
			Path:    "status",
			TSValue: strconv.Itoa(tsResp.Status),
			GoValue: strconv.Itoa(goResp.Status),
			Detail:  "HTTP status mismatch",
		})
	}

	findings = append(findings, d.diffHTTPHeaders(step, tsResp, goResp)...)
	if tsResp.Status >= 300 && tsResp.Status < 400 && goResp.Status == tsResp.Status {
		// On 3xx redirects, clients follow Location + Set-Cookie (both strictly
		// diffed above); Express plain-text vs Go HTML redirect fallback text is free.
		return findings
	}
	if tsResp.Status >= 400 && goResp.Status >= 400 && d.Ledger.shouldIgnoreErrorBody(step) {
		return findings
	}
	return append(findings, d.diffHTTPBody(step, tsResp, goResp)...)
}

func (d *Differ) diffHTTPHeaders(step string, tsResp, goResp Response) []Finding {
	var findings []Finding
	tsCT := headerGet(tsResp.Header, "Content-Type")
	goCT := headerGet(goResp.Header, "Content-Type")
	isRedirect := tsResp.Status >= 300 && tsResp.Status < 400 && goResp.Status == tsResp.Status
	ignoreErrCT := (isRedirect || (tsResp.Status >= 400 && goResp.Status >= 400 && d.Ledger.shouldIgnoreErrorBody(step))) &&
		isNonJSONErrorContentType(tsCT) && isNonJSONErrorContentType(goCT)
	if !ignoreErrCT && !equalContentType(tsCT, goCT) {
		findings = append(findings, Finding{
			Step:    step,
			Surface: "http",
			Path:    "header.Content-Type",
			TSValue: tsCT,
			GoValue: goCT,
			Detail:  "Content-Type mismatch",
		})
	}

	contractHeaders := []string{
		"Content-Range",
		"Accept-Ranges",
		"Content-Disposition",
		"Location",
		"Access-Control-Allow-Origin",
		"Access-Control-Allow-Credentials",
	}
	for _, name := range contractHeaders {
		tsVal := d.Corr.NormalizeString("ts", headerGet(tsResp.Header, name))
		goVal := d.Corr.NormalizeString("go", headerGet(goResp.Header, name))
		if tsVal != goVal {
			findings = append(findings, Finding{
				Step:    step,
				Surface: "http",
				Path:    "header." + name,
				TSValue: tsVal,
				GoValue: goVal,
				Detail:  "contract header mismatch",
			})
		}
	}

	return append(findings, d.diffCookies(step, extractCookies(tsResp), extractCookies(goResp))...)
}

func extractCookies(resp Response) []*http.Cookie {
	if len(resp.Cookies) > 0 {
		return resp.Cookies
	}
	if len(resp.Header) == 0 || len(resp.Header.Values("Set-Cookie")) == 0 {
		return nil
	}
	return (&http.Response{Header: resp.Header}).Cookies()
}

func (d *Differ) diffCookies(step string, tsCookies, goCookies []*http.Cookie) []Finding {
	tsMap := make(map[string]*http.Cookie, len(tsCookies))
	goMap := make(map[string]*http.Cookie, len(goCookies))
	nameSet := make(map[string]struct{})
	for _, c := range tsCookies {
		tsMap[c.Name] = c
		nameSet[c.Name] = struct{}{}
	}
	for _, c := range goCookies {
		goMap[c.Name] = c
		nameSet[c.Name] = struct{}{}
	}
	names := make([]string, 0, len(nameSet))
	for name := range nameSet {
		names = append(names, name)
	}
	sort.Strings(names)

	var findings []Finding
	for _, name := range names {
		p := "cookie." + name
		tc, okTS := tsMap[name]
		gc, okGo := goMap[name]
		if !okTS {
			findings = append(findings, Finding{Step: step, Surface: "http", Path: p, TSValue: "<missing>", GoValue: gc.Name, Detail: "extra Set-Cookie"})
			continue
		}
		if !okGo {
			findings = append(findings, Finding{Step: step, Surface: "http", Path: p, TSValue: tc.Name, GoValue: "<missing>", Detail: "missing Set-Cookie"})
			continue
		}
		if tc.HttpOnly != gc.HttpOnly {
			findings = append(findings, Finding{Step: step, Surface: "http", Path: p + ".httpOnly", TSValue: strconv.FormatBool(tc.HttpOnly), GoValue: strconv.FormatBool(gc.HttpOnly), Detail: "cookie HttpOnly mismatch"})
		}
		if tc.SameSite != gc.SameSite {
			findings = append(findings, Finding{Step: step, Surface: "http", Path: p + ".sameSite", TSValue: strconv.Itoa(int(tc.SameSite)), GoValue: strconv.Itoa(int(gc.SameSite)), Detail: "cookie SameSite mismatch"})
		}
		tsPath := d.Corr.NormalizeString("ts", tc.Path)
		goPath := d.Corr.NormalizeString("go", gc.Path)
		if tsPath != goPath {
			findings = append(findings, Finding{Step: step, Surface: "http", Path: p + ".path", TSValue: tsPath, GoValue: goPath, Detail: "cookie Path mismatch"})
		}
		findings = append(findings, d.diffStringValue(step, "http", p+".value", tc.Value, gc.Value)...)
	}
	return findings
}

func (d *Differ) diffHTTPBody(step string, tsResp, goResp Response) []Finding {
	if len(tsResp.Body) == 0 && len(goResp.Body) == 0 {
		return nil
	}
	if len(tsResp.Body) == 0 || len(goResp.Body) == 0 {
		return []Finding{{
			Step:    step,
			Surface: "http",
			Path:    "body",
			TSValue: fmt.Sprintf("%d bytes", len(tsResp.Body)),
			GoValue: fmt.Sprintf("%d bytes", len(goResp.Body)),
			Detail:  "one HTTP body is empty",
		}}
	}

	tsCT := headerGet(tsResp.Header, "Content-Type")
	goCT := headerGet(goResp.Header, "Content-Type")
	if isJSONContentType(tsCT) || isJSONContentType(goCT) || (json.Valid(tsResp.Body) && json.Valid(goResp.Body)) {
		tsVal, tsErr := decodeJSONBytes(tsResp.Body)
		goVal, goErr := decodeJSONBytes(goResp.Body)
		if tsErr == nil && goErr == nil {
			return d.diffJSON(step, "http", "body", tsVal, goVal)
		}
	}

	if isTextBody(tsCT, tsResp.Body) && isTextBody(goCT, goResp.Body) {
		tsText := d.Corr.NormalizeString("ts", string(tsResp.Body))
		goText := d.Corr.NormalizeString("go", string(goResp.Body))
		isXML := isXMLBody(tsCT, tsResp.Body) && isXMLBody(goCT, goResp.Body)
		if isXML {
			tsText = normalizeXML(tsText)
			goText = normalizeXML(goText)
			if tsText != goText && equalXMLWithPubDateTolerance(tsText, goText, 5*time.Second) {
				return nil
			}
		}
		if tsText != goText {
			return []Finding{{
				Step:    step,
				Surface: "http",
				Path:    "body",
				TSValue: truncateText(tsText, 200),
				GoValue: truncateText(goText, 200),
				Detail:  "text body mismatch",
			}}
		}
		return nil
	}

	return d.diffBinary(step, "http", "body", tsResp.Body, goResp.Body)
}

var zipMagic = []byte{0x50, 0x4b, 0x03, 0x04}

func (d *Differ) diffBinary(step, surface, path string, tsBytes, goBytes []byte) []Finding {
	tsHash := sha256.Sum256(tsBytes)
	goHash := sha256.Sum256(goBytes)
	if tsHash == goHash {
		return nil
	}
	if bytes.HasPrefix(tsBytes, zipMagic) && bytes.HasPrefix(goBytes, zipMagic) {
		return d.diffZip(step, surface, path, tsBytes, goBytes)
	}
	tsMagic := magicHex(tsBytes)
	goMagic := magicHex(goBytes)
	return []Finding{{
		Step:    step,
		Surface: surface,
		Path:    path,
		TSValue: fmt.Sprintf("len=%d magic=%s sha256=%x", len(tsBytes), tsMagic, tsHash[:8]),
		GoValue: fmt.Sprintf("len=%d magic=%s sha256=%x", len(goBytes), goMagic, goHash[:8]),
		Detail:  "binary payload mismatch",
	}}
}

func (d *Differ) diffZip(step, surface, path string, tsBytes, goBytes []byte) []Finding {
	tsNames, tsErr := d.inspectZipEntries("ts", tsBytes)
	goNames, goErr := d.inspectZipEntries("go", goBytes)
	if tsErr != "" || goErr != "" {
		return []Finding{{
			Step:    step,
			Surface: surface,
			Path:    path,
			TSValue: tsErr,
			GoValue: goErr,
			Detail:  "zip archive validation failed",
		}}
	}
	if !slices.Equal(tsNames, goNames) {
		return []Finding{{
			Step:    step,
			Surface: surface,
			Path:    path,
			TSValue: strings.Join(tsNames, ", "),
			GoValue: strings.Join(goNames, ", "),
			Detail:  "zip entry list mismatch",
		}}
	}
	return nil
}

func (d *Differ) inspectZipEntries(side string, data []byte) ([]string, string) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Sprintf("invalid zip: %v", err)
	}
	var names []string
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Sprintf("open zip entry %q: %v", f.Name, err)
		}
		n, copyErr := io.Copy(io.Discard, rc)
		closeErr := rc.Close()
		if copyErr != nil {
			return nil, fmt.Sprintf("read zip entry %q: %v", f.Name, copyErr)
		}
		if closeErr != nil {
			return nil, fmt.Sprintf("close zip entry %q: %v", f.Name, closeErr)
		}
		if f.FileInfo().IsDir() || strings.HasSuffix(f.Name, "/") {
			continue
		}
		if n == 0 || f.UncompressedSize64 == 0 {
			return nil, fmt.Sprintf("empty zip entry %q", f.Name)
		}
		names = append(names, d.Corr.NormalizeString(side, f.Name))
	}
	sort.Strings(names)
	return names, ""
}

func magicHex(b []byte) string {
	n := min(len(b), 8)
	return hex.EncodeToString(b[:n])
}

func (d *Differ) diffJSON(step, surface, path string, tsVal, goVal any) []Finding {
	if tsVal == nil && goVal == nil {
		return nil
	}
	if tsVal == nil || goVal == nil {
		return []Finding{{
			Step:    step,
			Surface: surface,
			Path:    path,
			TSValue: summarizeValue(tsVal),
			GoValue: summarizeValue(goVal),
			Detail:  fmt.Sprintf("null vs %s mismatch", jsonKind(nonNil(tsVal, goVal))),
		}}
	}

	switch tv := tsVal.(type) {
	case map[string]any:
		gv, ok := goVal.(map[string]any)
		if !ok {
			return []Finding{typeMismatchFinding(step, surface, path, tsVal, goVal)}
		}
		// The "discover" personalized shelf uses ORDER BY RANDOM() on both TS and Go;
		// sort its entities by ID so the set of items is compared deterministically.
		if tv["id"] == "discover" && gv["id"] == "discover" {
			if te, ok1 := tv["entities"].([]any); ok1 {
				if ge, ok2 := gv["entities"].([]any); ok2 {
					tvCopy := maps.Clone(tv)
					gvCopy := maps.Clone(gv)
					tvCopy["entities"] = sortJSONObjectsByID(d.Corr, "ts", te)
					gvCopy["entities"] = sortJSONObjectsByID(d.Corr, "go", ge)
					return d.diffJSONObject(step, surface, path, tvCopy, gvCopy)
				}
			}
		}
		return d.diffJSONObject(step, surface, path, tv, gv)
	case []any:
		gv, ok := goVal.([]any)
		if !ok {
			return []Finding{typeMismatchFinding(step, surface, path, tsVal, goVal)}
		}
		if path == "body.currentDailyLogs" {
			return diffDailyLogsArray(step, surface, path, tv, gv)
		}
		if len(tv) != len(gv) {
			return []Finding{{
				Step:    step,
				Surface: surface,
				Path:    path,
				TSValue: fmt.Sprintf("len=%d", len(tv)),
				GoValue: fmt.Sprintf("len=%d", len(gv)),
				Detail:  "array length mismatch",
			}}
		}
		tsArr, goArr := tv, gv
		switch path {
		case "body.usersOnline":
			tsArr = sortJSONObjectsByID(d.Corr, "ts", tv)
			goArr = sortJSONObjectsByID(d.Corr, "go", gv)
		case "body.booksAddedWithCovers":
			tsArr = sortJSONStrings(d.Corr, "ts", tv)
			goArr = sortJSONStrings(d.Corr, "go", gv)
		}
		var findings []Finding
		for i := range tsArr {
			elemPath := fmt.Sprintf("%s[%d]", path, i)
			findings = append(findings, d.diffJSON(step, surface, elemPath, tsArr[i], goArr[i])...)
		}
		return findings
	case bool:
		gv, ok := goVal.(bool)
		if !ok {
			return []Finding{typeMismatchFinding(step, surface, path, tsVal, goVal)}
		}
		if tv != gv {
			return []Finding{{
				Step:    step,
				Surface: surface,
				Path:    path,
				TSValue: strconv.FormatBool(tv),
				GoValue: strconv.FormatBool(gv),
				Detail:  "boolean mismatch",
			}}
		}
		return nil
	case json.Number:
		gv, ok := goVal.(json.Number)
		if !ok {
			return []Finding{typeMismatchFinding(step, surface, path, tsVal, goVal)}
		}
		return d.diffJSONNumber(step, surface, path, tv, gv)
	case string:
		gv, ok := goVal.(string)
		if !ok {
			return []Finding{typeMismatchFinding(step, surface, path, tsVal, goVal)}
		}
		return d.diffStringValue(step, surface, path, tv, gv)
	default:
		return []Finding{typeMismatchFinding(step, surface, path, tsVal, goVal)}
	}
}

func (d *Differ) diffJSONObject(step, surface, path string, tsMap, goMap map[string]any) []Finding {
	keySet := make(map[string]struct{}, len(tsMap)+len(goMap))
	for k := range tsMap {
		keySet[k] = struct{}{}
	}
	for k := range goMap {
		keySet[k] = struct{}{}
	}
	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var findings []Finding
	for _, k := range keys {
		childPath := joinPath(path, k)
		tv, okTS := tsMap[k]
		gv, okGo := goMap[k]
		if !okTS {
			findings = append(findings, Finding{
				Step:    step,
				Surface: surface,
				Path:    childPath,
				TSValue: "<missing>",
				GoValue: summarizeValue(gv),
				Detail:  "extra field in Go",
			})
			continue
		}
		if !okGo {
			findings = append(findings, Finding{
				Step:    step,
				Surface: surface,
				Path:    childPath,
				TSValue: summarizeValue(tv),
				GoValue: "<missing>",
				Detail:  "missing field in Go",
			})
			continue
		}
		findings = append(findings, d.diffJSON(step, surface, childPath, tv, gv)...)
	}
	return findings
}

func diffDailyLogsArray(step, surface, path string, tv, gv []any) []Finding {
	if len(tv) == 0 || len(gv) == 0 {
		return []Finding{{
			Step:    step,
			Surface: surface,
			Path:    path,
			TSValue: fmt.Sprintf("len=%d", len(tv)),
			GoValue: fmt.Sprintf("len=%d", len(gv)),
			Detail:  "expected non-empty daily logs array",
		}}
	}
	for i, elem := range tv {
		if _, ok := elem.(map[string]any); !ok {
			return []Finding{{
				Step:    step,
				Surface: surface,
				Path:    fmt.Sprintf("%s[%d]", path, i),
				TSValue: summarizeValue(elem),
				GoValue: "object",
				Detail:  "expected object element in daily logs array",
			}}
		}
	}
	for i, elem := range gv {
		if _, ok := elem.(map[string]any); !ok {
			return []Finding{{
				Step:    step,
				Surface: surface,
				Path:    fmt.Sprintf("%s[%d]", path, i),
				TSValue: "object",
				GoValue: summarizeValue(elem),
				Detail:  "expected object element in daily logs array",
			}}
		}
	}
	return nil
}

func sortJSONObjectsByID(corr *Correspondence, side string, items []any) []any {
	out := slices.Clone(items)
	sort.SliceStable(out, func(i, j int) bool {
		return jsonObjectIDKey(corr, side, out[i]) < jsonObjectIDKey(corr, side, out[j])
	})
	return out
}

func jsonObjectIDKey(corr *Correspondence, side string, v any) string {
	obj, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	id, ok := obj["id"].(string)
	if !ok {
		return ""
	}
	return corr.NormalizeString(side, id)
}

func sortJSONStrings(corr *Correspondence, side string, items []any) []any {
	out := slices.Clone(items)
	sort.SliceStable(out, func(i, j int) bool {
		si, _ := out[i].(string)
		sj, _ := out[j].(string)
		return corr.NormalizeString(side, si) < corr.NormalizeString(side, sj)
	})
	return out
}

func (d *Differ) diffInode(step, surface, path, tsStr, goStr string) []Finding {
	tsNorm := d.Corr.NormalizeString("ts", tsStr)
	goNorm := d.Corr.NormalizeString("go", goStr)
	if tsNorm == goNorm {
		return nil
	}
	if !d.Corr.HasInode("ts", tsStr) && !d.Corr.HasInode("go", goStr) &&
		isNumericString(tsStr) && isNumericString(goStr) {
		return nil
	}
	return []Finding{{
		Step:    step,
		Surface: surface,
		Path:    path,
		TSValue: tsNorm,
		GoValue: goNorm,
		Detail:  "inode mismatch",
	}}
}

func isNumericString(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if !isASCIIDigit(s[i]) {
			return false
		}
	}
	return true
}

func extractFeedItemUUID(s string) (prefix, uuid, suffix string, ok bool) {
	feedIdx := strings.Index(s, "/feed/")
	if feedIdx < 0 {
		return "", "", "", false
	}
	relItem := strings.Index(s[feedIdx+len("/feed/"):], "/item/")
	if relItem < 0 {
		return "", "", "", false
	}
	uuidStart := feedIdx + len("/feed/") + relItem + len("/item/")
	if len(s) < uuidStart+36 {
		return "", "", "", false
	}
	cand := s[uuidStart : uuidStart+36]
	if !isUUID(cand) {
		return "", "", "", false
	}
	if len(s) > uuidStart+36 && s[uuidStart+36] != '/' {
		return "", "", "", false
	}
	return s[:uuidStart], cand, s[uuidStart+36:], true
}

func (d *Differ) diffJSONNumber(step, surface, path string, tv, gv json.Number) []Finding {
	key := lastKeyName(path)
	if key == "ino" {
		return d.diffInode(step, surface, path, tv.String(), gv.String())
	}

	tf, errT := tv.Float64()
	gf, errG := gv.Float64()
	if errT != nil || errG != nil {
		if tv.String() != gv.String() {
			return []Finding{{
				Step:    step,
				Surface: surface,
				Path:    path,
				TSValue: tv.String(),
				GoValue: gv.String(),
				Detail:  "number mismatch",
			}}
		}
		return nil
	}

	if isTimestampKey(key) {
		if (tf == 0) != (gf == 0) || tf < 0 || gf < 0 {
			return []Finding{{
				Step:    step,
				Surface: surface,
				Path:    path,
				TSValue: tv.String(),
				GoValue: gv.String(),
				Detail:  "timestamp number mismatch",
			}}
		}
		if !sameTimestampScale(tf, gf) {
			return []Finding{{
				Step:    step,
				Surface: surface,
				Path:    path,
				TSValue: tv.String(),
				GoValue: gv.String(),
				Detail:  "timestamp scale mismatch",
			}}
		}
		return nil
	}

	if ti, err1 := tv.Int64(); err1 == nil {
		if gi, err2 := gv.Int64(); err2 == nil {
			if key == "port" && d.Corr.MatchStubSMTPPort(ti, gi) {
				return nil
			}
			if strings.Contains(path, ".backups[") && strings.HasSuffix(path, ".fileSize") && ti > 0 && gi > 0 {
				lo, hi := min(ti, gi), max(ti, gi)
				if float64(hi-lo) <= 0.5*float64(hi) {
					return nil
				}
			}
			if ti != gi {
				return []Finding{{
					Step:    step,
					Surface: surface,
					Path:    path,
					TSValue: tv.String(),
					GoValue: gv.String(),
					Detail:  "integer mismatch",
				}}
			}
			return nil
		}
	}
	if tf != gf {
		return []Finding{{
			Step:    step,
			Surface: surface,
			Path:    path,
			TSValue: tv.String(),
			GoValue: gv.String(),
			Detail:  "number mismatch",
		}}
	}
	return nil
}

func (d *Differ) diffStringValue(step, surface, path, tsStr, goStr string) []Finding {
	tsJWT, okTSJWT := parseJWT(tsStr)
	goJWT, okGoJWT := parseJWT(goStr)
	if okTSJWT && okGoJWT {
		return d.diffJWT(step, surface, path, tsJWT, goJWT)
	}
	if okTSJWT != okGoJWT {
		return []Finding{{
			Step:    step,
			Surface: surface,
			Path:    path,
			TSValue: truncateText(tsStr, 80),
			GoValue: truncateText(goStr, 80),
			Detail:  "JWT format mismatch",
		}}
	}

	key := lastKeyName(path)
	if key == "ino" {
		return d.diffInode(step, surface, path, tsStr, goStr)
	}
	if key == "port" {
		if tp, errT := strconv.ParseInt(tsStr, 10, 64); errT == nil {
			if gp, errG := strconv.ParseInt(goStr, 10, 64); errG == nil {
				if d.Corr.MatchStubSMTPPort(tp, gp) {
					return nil
				}
			}
		}
	}
	if isTimestampKey(key) {
		if tsStr == goStr {
			return nil
		}
		if !isValidTimestampString(tsStr) || !isValidTimestampString(goStr) {
			return []Finding{{
				Step:    step,
				Surface: surface,
				Path:    path,
				TSValue: tsStr,
				GoValue: goStr,
				Detail:  "invalid timestamp string",
			}}
		}
		return nil
	}

	if tsPre, tsUUID, tsSuf, okTS := extractFeedItemUUID(tsStr); okTS {
		if goPre, goUUID, goSuf, okGo := extractFeedItemUUID(goStr); okGo {
			if d.Corr.NormalizeString("ts", tsPre) == d.Corr.NormalizeString("go", goPre) &&
				d.Corr.NormalizeString("ts", tsSuf) == d.Corr.NormalizeString("go", goSuf) {
				if !d.Corr.BindPair(tsUUID, goUUID) {
					return []Finding{{
						Step:    step,
						Surface: surface,
						Path:    path,
						TSValue: tsStr,
						GoValue: goStr,
						Detail:  "bijective UUID correspondence mismatch",
					}}
				}
				return nil
			}
		}
	}

	if isBcryptHash(tsStr) && isBcryptHash(goStr) {
		return nil
	}

	if isUUID(tsStr) && isUUID(goStr) {
		if !d.Corr.BindPair(tsStr, goStr) {
			return []Finding{{
				Step:    step,
				Surface: surface,
				Path:    path,
				TSValue: tsStr,
				GoValue: goStr,
				Detail:  "bijective UUID correspondence mismatch",
			}}
		}
		return nil
	}

	if mappedGo, hasTS := d.Corr.MapForGo(tsStr); hasTS {
		if mappedGo != goStr {
			return []Finding{{
				Step:    step,
				Surface: surface,
				Path:    path,
				TSValue: tsStr,
				GoValue: goStr,
				Detail:  "bound ID mismatch",
			}}
		}
		return nil
	}
	if mappedTS, hasGo := d.Corr.MapForTS(goStr); hasGo && mappedTS != tsStr {
		return []Finding{{
			Step:    step,
			Surface: surface,
			Path:    path,
			TSValue: tsStr,
			GoValue: goStr,
			Detail:  "bound ID mismatch",
		}}
	}

	if isEphemeralKey(key, tsStr, goStr) {
		if tsStr == "" || goStr == "" || tokenFormat(tsStr) != tokenFormat(goStr) {
			return []Finding{{
				Step:    step,
				Surface: surface,
				Path:    path,
				TSValue: tsStr,
				GoValue: goStr,
				Detail:  "ephemeral token format mismatch",
			}}
		}
		return nil
	}

	tsNorm := d.Corr.NormalizeString("ts", tsStr)
	goNorm := d.Corr.NormalizeString("go", goStr)
	if tsNorm != goNorm {
		return []Finding{{
			Step:    step,
			Surface: surface,
			Path:    path,
			TSValue: tsNorm,
			GoValue: goNorm,
			Detail:  "string mismatch",
		}}
	}
	return nil
}

type parsedJWT struct {
	header  map[string]any
	payload map[string]any
	hasSig  bool
}

func parseJWT(s string) (parsedJWT, bool) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return parsedJWT{}, false
	}
	hBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return parsedJWT{}, false
	}
	pBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return parsedJWT{}, false
	}
	hVal, err := decodeJSONBytes(hBytes)
	if err != nil {
		return parsedJWT{}, false
	}
	pVal, err := decodeJSONBytes(pBytes)
	if err != nil {
		return parsedJWT{}, false
	}
	hMap, okH := hVal.(map[string]any)
	pMap, okP := pVal.(map[string]any)
	if !okH || !okP {
		return parsedJWT{}, false
	}
	return parsedJWT{header: hMap, payload: pMap, hasSig: parts[2] != ""}, true
}

func (d *Differ) diffJWT(step, surface, path string, tsJWT, goJWT parsedJWT) []Finding {
	findings := d.diffJSONObject(step, surface, path+".header", tsJWT.header, goJWT.header)

	tsPayload := cloneMapWithout(tsJWT.payload, "iat", "exp")
	goPayload := cloneMapWithout(goJWT.payload, "iat", "exp")
	findings = append(findings, d.diffJSONObject(step, surface, path+".payload", tsPayload, goPayload)...)

	findings = append(findings, diffJWTNumericClaim(step, surface, path+".payload.iat", tsJWT.payload["iat"], goJWT.payload["iat"])...)
	findings = append(findings, diffJWTNumericClaim(step, surface, path+".payload.exp", tsJWT.payload["exp"], goJWT.payload["exp"])...)

	tsIat, okTI := numberFloat(tsJWT.payload["iat"])
	tsExp, okTE := numberFloat(tsJWT.payload["exp"])
	goIat, okGI := numberFloat(goJWT.payload["iat"])
	goExp, okGE := numberFloat(goJWT.payload["exp"])
	if okTI && okTE && okGI && okGE {
		tsTTL := tsExp - tsIat
		goTTL := goExp - goIat
		if math.Abs(tsTTL-goTTL) > 1 {
			findings = append(findings, Finding{
				Step:    step,
				Surface: surface,
				Path:    path + ".payload.ttl",
				TSValue: fmt.Sprintf("%.0f", tsTTL),
				GoValue: fmt.Sprintf("%.0f", goTTL),
				Detail:  "JWT exp-iat delta mismatch",
			})
		}
	}
	return findings
}

func diffJWTNumericClaim(step, surface, path string, tsVal, goVal any) []Finding {
	if tsVal == nil && goVal == nil {
		return nil
	}
	if tsVal == nil || goVal == nil {
		return []Finding{{
			Step:    step,
			Surface: surface,
			Path:    path,
			TSValue: summarizeValue(tsVal),
			GoValue: summarizeValue(goVal),
			Detail:  "JWT claim presence mismatch",
		}}
	}
	tf, okT := numberFloat(tsVal)
	gf, okG := numberFloat(goVal)
	if !okT || !okG || tf <= 0 || gf <= 0 {
		return []Finding{{
			Step:    step,
			Surface: surface,
			Path:    path,
			TSValue: summarizeValue(tsVal),
			GoValue: summarizeValue(goVal),
			Detail:  "invalid JWT timestamp claim",
		}}
	}
	return nil
}

func cloneMapWithout(src map[string]any, skip ...string) map[string]any {
	out := make(map[string]any, len(src))
	for k, v := range src {
		excluded := false
		for _, s := range skip {
			if k == s {
				excluded = true
				break
			}
		}
		if !excluded {
			out[k] = v
		}
	}
	return out
}

func numberFloat(v any) (float64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := n.Float64()
	return f, err == nil
}

// DiffSockets groups Socket.IO events by (Role, Name), verifies matching counts,
// and recursively compares normalized JSON payloads in stream order.
func (d *Differ) DiffSockets(step string, tsEvents, goEvents []SocketEvent) []Finding {
	type streamKey struct {
		Role string
		Name string
	}
	tsGrouped := make(map[streamKey][]SocketEvent)
	goGrouped := make(map[streamKey][]SocketEvent)
	keySet := make(map[streamKey]struct{})

	for _, ev := range tsEvents {
		k := streamKey{Role: ev.Role, Name: ev.Name}
		tsGrouped[k] = append(tsGrouped[k], ev)
		keySet[k] = struct{}{}
	}
	for _, ev := range goEvents {
		k := streamKey{Role: ev.Role, Name: ev.Name}
		goGrouped[k] = append(goGrouped[k], ev)
		keySet[k] = struct{}{}
	}

	keys := make([]streamKey, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Role != keys[j].Role {
			return keys[i].Role < keys[j].Role
		}
		return keys[i].Name < keys[j].Name
	})

	var findings []Finding
	for _, k := range keys {
		tsList := tsGrouped[k]
		goList := goGrouped[k]
		basePath := k.Role + ":" + k.Name
		if len(tsList) != len(goList) {
			findings = append(findings, Finding{
				Step:    step,
				Surface: "socket",
				Path:    basePath,
				TSValue: fmt.Sprintf("%d events", len(tsList)),
				GoValue: fmt.Sprintf("%d events", len(goList)),
				Detail:  "socket event count mismatch",
			})
		}
		n := min(len(tsList), len(goList))
		for i := range n {
			tsArgs := tsList[i].Args
			goArgs := goList[i].Args
			evPath := fmt.Sprintf("%s[%d]", basePath, i)
			if len(tsArgs) != len(goArgs) {
				findings = append(findings, Finding{
					Step:    step,
					Surface: "socket",
					Path:    evPath + ".args",
					TSValue: fmt.Sprintf("%d args", len(tsArgs)),
					GoValue: fmt.Sprintf("%d args", len(goArgs)),
					Detail:  "socket argument count mismatch",
				})
				continue
			}
			for j := range tsArgs {
				argPath := fmt.Sprintf("%s.args[%d]", evPath, j)
				tsVal, tsErr := decodeJSONBytes(tsArgs[j])
				goVal, goErr := decodeJSONBytes(goArgs[j])
				if tsErr == nil && goErr == nil {
					findings = append(findings, d.diffJSON(step, "socket", argPath, tsVal, goVal)...)
					continue
				}
				tsNorm := d.Corr.NormalizeString("ts", string(tsArgs[j]))
				goNorm := d.Corr.NormalizeString("go", string(goArgs[j]))
				if tsNorm != goNorm {
					findings = append(findings, Finding{
						Step:    step,
						Surface: "socket",
						Path:    argPath,
						TSValue: tsNorm,
						GoValue: goNorm,
						Detail:  "raw socket arg mismatch",
					})
				}
			}
		}
	}
	return findings
}

// DiffStubs compares count, kind, method, normalized path, and normalized body
// of outbound stub calls.
func (d *Differ) DiffStubs(step string, tsCalls, goCalls []StubCall) []Finding {
	var findings []Finding
	if len(tsCalls) != len(goCalls) {
		findings = append(findings, Finding{
			Step:    step,
			Surface: "stubs",
			Path:    "calls.count",
			TSValue: strconv.Itoa(len(tsCalls)),
			GoValue: strconv.Itoa(len(goCalls)),
			Detail:  "outbound stub call count mismatch",
		})
	}
	n := min(len(tsCalls), len(goCalls))
	for i := range n {
		prefix := fmt.Sprintf("calls[%d]", i)
		tc := tsCalls[i]
		gc := goCalls[i]
		if tc.Kind != gc.Kind {
			findings = append(findings, Finding{Step: step, Surface: "stubs", Path: prefix + ".kind", TSValue: tc.Kind, GoValue: gc.Kind, Detail: "stub kind mismatch"})
		}
		if !strings.EqualFold(tc.Method, gc.Method) {
			findings = append(findings, Finding{Step: step, Surface: "stubs", Path: prefix + ".method", TSValue: tc.Method, GoValue: gc.Method, Detail: "stub method mismatch"})
		}
		tsPath := d.Corr.NormalizeString("ts", tc.Path)
		goPath := d.Corr.NormalizeString("go", gc.Path)
		if strings.EqualFold(tc.Kind, "http") && strings.EqualFold(gc.Kind, "http") {
			tsPath = canonicalStubPath(tsPath)
			goPath = canonicalStubPath(goPath)
		}
		if tsPath != goPath {
			findings = append(findings, Finding{Step: step, Surface: "stubs", Path: prefix + ".path", TSValue: tsPath, GoValue: goPath, Detail: "stub path mismatch"})
		}
		if tc.Body != "" || gc.Body != "" {
			tsVal, tsErr := decodeJSONBytes([]byte(tc.Body))
			goVal, goErr := decodeJSONBytes([]byte(gc.Body))
			if tsErr == nil && goErr == nil {
				findings = append(findings, d.diffJSON(step, "stubs", prefix+".body", tsVal, goVal)...)
			} else if isSMTPEmailStub(tc, gc) {
				findings = append(findings, d.diffSMTPEmail(step, prefix+".body", tc.Body, gc.Body)...)
			} else {
				tsBody := d.Corr.NormalizeString("ts", tc.Body)
				goBody := d.Corr.NormalizeString("go", gc.Body)
				if tsBody != goBody {
					findings = append(findings, Finding{Step: step, Surface: "stubs", Path: prefix + ".body", TSValue: tsBody, GoValue: goBody, Detail: "stub body mismatch"})
				}
			}
		}
	}
	return findings
}

// FormatFindings renders findings into a deterministic multi-line diagnostic report.
func FormatFindings(findings []Finding) string {
	if len(findings) == 0 {
		return ""
	}
	var b strings.Builder
	for i, f := range findings {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "[%s] step=%s path=%s ts=%q go=%q (%s)", f.Surface, f.Step, f.Path, f.TSValue, f.GoValue, f.Detail)
	}
	return b.String()
}

func decodeJSONBytes(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("trailing tokens after JSON value")
	}
	return v, nil
}

func headerGet(h http.Header, key string) string {
	if len(h) == 0 {
		return ""
	}
	return h.Get(key)
}

func equalContentType(tsCT, goCT string) bool {
	if tsCT == "" && goCT == "" {
		return true
	}
	tsMedia, tsParams, tsErr := mime.ParseMediaType(tsCT)
	goMedia, goParams, goErr := mime.ParseMediaType(goCT)
	if tsErr != nil || goErr != nil {
		return strings.TrimSpace(tsCT) == strings.TrimSpace(goCT)
	}
	return strings.EqualFold(tsMedia, goMedia) && strings.EqualFold(tsParams["charset"], goParams["charset"])
}

func isJSONContentType(ct string) bool {
	media, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return strings.Contains(strings.ToLower(ct), "json")
	}
	return strings.Contains(strings.ToLower(media), "json")
}

func isTextBody(ct string, body []byte) bool {
	lower := strings.ToLower(ct)
	if strings.HasPrefix(lower, "text/") || strings.Contains(lower, "xml") || strings.Contains(lower, "mpegurl") || strings.Contains(lower, "javascript") {
		return true
	}
	return ct == "" && utf8.Valid(body) && !bytes.ContainsRune(body, 0)
}

func typeMismatchFinding(step, surface, path string, tsVal, goVal any) Finding {
	return Finding{
		Step:    step,
		Surface: surface,
		Path:    path,
		TSValue: summarizeValue(tsVal),
		GoValue: summarizeValue(goVal),
		Detail:  fmt.Sprintf("type mismatch: %s vs %s", jsonKind(tsVal), jsonKind(goVal)),
	}
}

func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case json.Number:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", v)
	}
}

func summarizeValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return truncateText(x, 120)
	case bool:
		return strconv.FormatBool(x)
	case json.Number:
		return x.String()
	case []any:
		return fmt.Sprintf("array(len=%d)", len(x))
	case map[string]any:
		return fmt.Sprintf("object(keys=%d)", len(x))
	default:
		return fmt.Sprintf("%v", v)
	}
}

func nonNil(a, b any) any {
	if a != nil {
		return a
	}
	return b
}

func joinPath(base, key string) string {
	if base == "" {
		return key
	}
	return base + "." + key
}

func lastKeyName(path string) string {
	idx := strings.LastIndex(path, ".")
	if idx >= 0 {
		path = path[idx+1:]
	}
	if b := strings.IndexByte(path, '['); b >= 0 {
		path = path[:b]
	}
	return path
}

func truncateText(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := range 36 {
		c := s[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func isTimestampKey(key string) bool {
	if key == "" {
		return false
	}
	if strings.HasSuffix(key, "At") {
		return true
	}
	lower := strings.ToLower(key)
	if strings.HasSuffix(lower, "_at") {
		return true
	}
	suffixes := []string{
		"time", "lastseen", "lastscan", "addedat", "updatedat",
		"createdat", "startedat", "finishedat", "mtimems",
		"birthtimems", "ctimems", "lastupdate", "lastepisodecheck",
	}
	for _, suf := range suffixes {
		if strings.HasSuffix(lower, suf) {
			return true
		}
	}
	return false
}

func sameTimestampScale(a, b float64) bool {
	if a == 0 && b == 0 {
		return true
	}
	// Reject seconds (~1e9) vs milliseconds (~1e12).
	ratio := a / b
	if ratio < 1 {
		ratio = b / a
	}
	return ratio < 10
}

func isValidTimestampString(s string) bool {
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999 -07:00",
		"2006-01-02 15:04:05 -07:00",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.999999999",
	}
	for _, layout := range layouts {
		if _, err := time.Parse(layout, s); err == nil {
			return true
		}
	}
	return false
}

func isEphemeralKey(key, tsVal, goVal string) bool {
	switch key {
	case "token", "accessToken", "refreshToken", "socketId":
		return true
	case "id":
		return isRandomOpaqueToken(tsVal) && isRandomOpaqueToken(goVal)
	default:
		return false
	}
}

func isRandomOpaqueToken(s string) bool {
	if len(s) < 16 || isUUID(s) {
		return false
	}
	for i := range len(s) {
		c := s[i]
		isValid := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '-' || c == '_' || c == '+' || c == '/' || c == '='
		if !isValid {
			return false
		}
	}
	return true
}

func tokenFormat(s string) string {
	if isUUID(s) {
		return "uuid"
	}
	if isHexString(s) {
		return fmt.Sprintf("hex:%d", len(s))
	}
	return "opaque"
}

func isHexString(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func isXMLBody(ct string, body []byte) bool {
	if strings.Contains(strings.ToLower(ct), "xml") {
		return true
	}
	return bytes.HasPrefix(bytes.TrimSpace(body), []byte("<?xml"))
}

// normalizeXML strips whitespace-only spans between XML tags, collapses empty
// elements (<tag ...></tag> -> <tag .../>), canonicalizes numeric quote
// entities outside CDATA/comments, and normalizes valid RFC1123
// <lastBuildDate> timestamps.
func normalizeXML(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if endIdx, ok := xmlVerbatimSpanEnd(s, i); ok {
			b.WriteString(s[i:endIdx])
			i = endIdx
			continue
		}
		if s[i] == '>' {
			b.WriteByte('>')
			j := i + 1
			for j < len(s) && isXMLWhitespace(s[j]) {
				j++
			}
			if j < len(s) && s[j] == '<' {
				i = j
				continue
			}
			i++
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return normalizeXMLPostPass(b.String())
}

// xmlVerbatimSpanEnd checks if s[i:] begins a CDATA section or XML comment and
// returns its exclusive end index.
func xmlVerbatimSpanEnd(s string, i int) (int, bool) {
	switch {
	case strings.HasPrefix(s[i:], "<![CDATA["):
		end := strings.Index(s[i+len("<![CDATA["):], "]]>")
		if end < 0 {
			return len(s), true
		}
		return i + len("<![CDATA[") + end + len("]]>"), true
	case strings.HasPrefix(s[i:], "<!--"):
		end := strings.Index(s[i+len("<!--"):], "-->")
		if end < 0 {
			return len(s), true
		}
		return i + len("<!--") + end + len("-->"), true
	default:
		return 0, false
	}
}

func normalizeXMLPostPass(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if endIdx, ok := xmlVerbatimSpanEnd(s, i); ok {
			b.WriteString(s[i:endIdx])
			i = endIdx
			continue
		}
		if strings.HasPrefix(s[i:], "<?") {
			end := strings.Index(s[i+2:], "?>")
			if end < 0 {
				b.WriteString(s[i:])
				break
			}
			endIdx := i + 2 + end + 2
			b.WriteString(s[i:endIdx])
			i = endIdx
			continue
		}
		if strings.HasPrefix(s[i:], "&#34;") {
			b.WriteString("&quot;")
			i += len("&#34;")
			continue
		}
		if strings.HasPrefix(s[i:], "&#39;") {
			b.WriteString("&apos;")
			i += len("&#39;")
			continue
		}
		if strings.HasPrefix(s[i:], "<lastBuildDate>") {
			closeIdx := strings.Index(s[i+len("<lastBuildDate>"):], "</lastBuildDate>")
			if closeIdx >= 0 {
				inner := s[i+len("<lastBuildDate>") : i+len("<lastBuildDate>")+closeIdx]
				if _, ok := parseRSSDate(inner); ok {
					b.WriteString("<lastBuildDate><rfc1123></lastBuildDate>")
					i += len("<lastBuildDate>") + closeIdx + len("</lastBuildDate>")
					continue
				}
			}
		}
		if s[i] == '<' && i+1 < len(s) && s[i+1] != '/' && s[i+1] != '!' {
			if tagName, gtIdx, ok := parseXMLStartTag(s, i); ok && gtIdx > i && s[gtIdx-1] != '/' {
				closing := "</" + tagName + ">"
				if strings.HasPrefix(s[gtIdx+1:], closing) {
					b.WriteString(s[i:gtIdx])
					b.WriteString("/>")
					i = gtIdx + 1 + len(closing)
					continue
				}
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func parseXMLStartTag(s string, start int) (tagName string, gtIdx int, ok bool) {
	if start >= len(s) || s[start] != '<' {
		return "", 0, false
	}
	nameStart := start + 1
	nameEnd := nameStart
	for nameEnd < len(s) && !isXMLWhitespace(s[nameEnd]) && s[nameEnd] != '>' && s[nameEnd] != '/' {
		nameEnd++
	}
	if nameEnd == nameStart {
		return "", 0, false
	}
	tagName = s[nameStart:nameEnd]
	var quote byte
	for j := nameEnd; j < len(s); j++ {
		c := s[j]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
			continue
		}
		if c == '>' {
			return tagName, j, true
		}
	}
	return "", 0, false
}

func parseRSSDate(raw string) (time.Time, bool) {
	trimmed := strings.TrimSpace(raw)
	for _, layout := range []string{time.RFC1123, time.RFC1123Z, "Mon, 2 Jan 2006 15:04:05 MST", "Mon, 2 Jan 2006 15:04:05 -0700"} {
		if t, err := time.Parse(layout, trimmed); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func equalXMLWithPubDateTolerance(tsXML, goXML string, tol time.Duration) bool {
	tsSkel, tsDates, okTS := extractXMLPubDates(tsXML)
	goSkel, goDates, okGo := extractXMLPubDates(goXML)
	if !okTS || !okGo || tsSkel != goSkel || len(tsDates) != len(goDates) {
		return false
	}
	for i := range tsDates {
		diff := tsDates[i].Sub(goDates[i])
		if diff < 0 {
			diff = -diff
		}
		if diff > tol {
			return false
		}
	}
	return true
}

func extractXMLPubDates(s string) (string, []time.Time, bool) {
	var b strings.Builder
	var dates []time.Time
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if endIdx, ok := xmlVerbatimSpanEnd(s, i); ok {
			b.WriteString(s[i:endIdx])
			i = endIdx
			continue
		}
		if strings.HasPrefix(s[i:], "<pubDate>") {
			closeIdx := strings.Index(s[i+len("<pubDate>"):], "</pubDate>")
			if closeIdx < 0 {
				return "", nil, false
			}
			inner := s[i+len("<pubDate>") : i+len("<pubDate>")+closeIdx]
			t, ok := parseRSSDate(inner)
			if !ok {
				return "", nil, false
			}
			dates = append(dates, t)
			b.WriteString("<pubDate><rfc1123></pubDate>")
			i += len("<pubDate>") + closeIdx + len("</pubDate>")
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String(), dates, true
}

func isXMLWhitespace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

func canonicalStubPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.RawQuery == "" {
		return raw
	}
	u.RawQuery = u.Query().Encode()
	return u.String()
}

func isSMTPEmailStub(tc, gc StubCall) bool {
	if strings.EqualFold(tc.Kind, "smtp") && strings.EqualFold(gc.Kind, "smtp") {
		return true
	}
	return strings.Contains(tc.Body, "MIME-Version: 1.0") && strings.Contains(gc.Body, "MIME-Version: 1.0")
}

type parsedEmailPart struct {
	Filename string
	Body     []byte
	IsText   bool
}

type parsedEmail struct {
	From    string
	To      string
	Subject string
	Parts   []parsedEmailPart
}

func (d *Differ) diffSMTPEmail(step, path, tsRaw, goRaw string) []Finding {
	tsEmail, tsErr := parseEmailMessage(tsRaw)
	goEmail, goErr := parseEmailMessage(goRaw)
	if tsErr != nil || goErr != nil {
		tsBody := d.Corr.NormalizeString("ts", tsRaw)
		goBody := d.Corr.NormalizeString("go", goRaw)
		if tsBody != goBody {
			return []Finding{{Step: step, Surface: "stubs", Path: path, TSValue: tsBody, GoValue: goBody, Detail: "stub body mismatch"}}
		}
		return nil
	}

	var findings []Finding
	findings = append(findings, d.diffStringValue(step, "stubs", path+".from", tsEmail.From, goEmail.From)...)
	findings = append(findings, d.diffStringValue(step, "stubs", path+".to", tsEmail.To, goEmail.To)...)
	findings = append(findings, d.diffStringValue(step, "stubs", path+".subject", tsEmail.Subject, goEmail.Subject)...)

	if len(tsEmail.Parts) != len(goEmail.Parts) {
		findings = append(findings, Finding{
			Step:    step,
			Surface: "stubs",
			Path:    path + ".parts.count",
			TSValue: strconv.Itoa(len(tsEmail.Parts)),
			GoValue: strconv.Itoa(len(goEmail.Parts)),
			Detail:  "MIME part count mismatch",
		})
	}
	n := min(len(tsEmail.Parts), len(goEmail.Parts))
	for i := range n {
		partPath := fmt.Sprintf("%s.parts[%d]", path, i)
		tp := tsEmail.Parts[i]
		gp := goEmail.Parts[i]
		findings = append(findings, d.diffStringValue(step, "stubs", partPath+".filename", tp.Filename, gp.Filename)...)
		if tp.IsText && gp.IsText {
			tsText := d.Corr.NormalizeString("ts", normalizeEmailText(tp.Body))
			goText := d.Corr.NormalizeString("go", normalizeEmailText(gp.Body))
			if tsText != goText {
				findings = append(findings, Finding{
					Step:    step,
					Surface: "stubs",
					Path:    partPath + ".body",
					TSValue: truncateText(tsText, 200),
					GoValue: truncateText(goText, 200),
					Detail:  "MIME text part mismatch",
				})
			}
			continue
		}
		if !bytes.Equal(tp.Body, gp.Body) {
			findings = append(findings, d.diffBinary(step, "stubs", partPath+".body", tp.Body, gp.Body)...)
		}
	}
	return findings
}

func parseEmailMessage(raw string) (*parsedEmail, error) {
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		return nil, err
	}
	from, err := normalizeEmailAddresses(msg.Header.Get("From"))
	if err != nil {
		return nil, err
	}
	to, err := normalizeEmailAddresses(msg.Header.Get("To"))
	if err != nil {
		return nil, err
	}
	parts, err := parseEmailParts(
		msg.Header.Get("Content-Type"),
		msg.Header.Get("Content-Transfer-Encoding"),
		msg.Header.Get("Content-Disposition"),
		msg.Body,
	)
	if err != nil {
		return nil, err
	}
	return &parsedEmail{
		From:    from,
		To:      to,
		Subject: decodeMIMEHeader(msg.Header.Get("Subject")),
		Parts:   parts,
	}, nil
}

func parseEmailParts(contentType, transferEncoding, contentDisposition string, body io.Reader) ([]parsedEmailPart, error) {
	mediaType := "text/plain"
	var params map[string]string
	if strings.TrimSpace(contentType) != "" {
		var err error
		mediaType, params, err = mime.ParseMediaType(contentType)
		if err != nil {
			return nil, err
		}
	}
	mediaLower := strings.ToLower(mediaType)
	if strings.HasPrefix(mediaLower, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return nil, fmt.Errorf("multipart missing boundary")
		}
		mr := multipart.NewReader(body, boundary)
		var out []parsedEmailPart
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				return out, nil
			}
			if err != nil {
				return nil, err
			}
			sub, err := parseEmailParts(
				part.Header.Get("Content-Type"),
				part.Header.Get("Content-Transfer-Encoding"),
				part.Header.Get("Content-Disposition"),
				part,
			)
			if err != nil {
				return nil, err
			}
			out = append(out, sub...)
		}
	}

	r := body
	switch strings.ToLower(strings.TrimSpace(transferEncoding)) {
	case "", "7bit", "8bit", "binary":
	case "base64":
		r = base64.NewDecoder(base64.StdEncoding, body)
	case "quoted-printable":
		r = quotedprintable.NewReader(body)
	default:
		return nil, fmt.Errorf("unsupported Content-Transfer-Encoding %q", transferEncoding)
	}
	decoded, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	var filename string
	if strings.TrimSpace(contentDisposition) != "" {
		_, dispParams, dispErr := mime.ParseMediaType(contentDisposition)
		if dispErr == nil && dispParams["filename"] != "" {
			filename = decodeMIMEHeader(dispParams["filename"])
		}
	}
	if filename == "" && params["name"] != "" {
		filename = decodeMIMEHeader(params["name"])
	}

	return []parsedEmailPart{{
		Filename: filename,
		Body:     decoded,
		IsText:   strings.HasPrefix(mediaLower, "text/") && filename == "",
	}}, nil
}

func normalizeEmailAddresses(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	addrs, err := mail.ParseAddressList(trimmed)
	if err != nil {
		addr, singleErr := mail.ParseAddress(trimmed)
		if singleErr != nil {
			return "", err
		}
		addrs = []*mail.Address{addr}
	}
	parts := make([]string, len(addrs))
	for i, a := range addrs {
		parts[i] = a.String()
	}
	return strings.Join(parts, ", "), nil
}

func decodeMIMEHeader(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var dec mime.WordDecoder
	decoded, err := dec.DecodeHeader(trimmed)
	if err != nil {
		return trimmed
	}
	return decoded
}

func normalizeEmailText(b []byte) string {
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	return strings.TrimRight(s, "\r\n")
}
