package oracle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
)

const (
	maxSchemaDepth      = 6
	maxSchemaProperties = 100
	maxInferJSONBytes   = 2 << 20
)

// HTTPObservation captures one observed TS HTTP request/response pair for OpenAPI synthesis.
type HTTPObservation struct {
	IsTS            bool
	Method          string
	Path            string
	Query           url.Values
	ReqContentType  string
	ReqBody         []byte
	Status          int
	RespContentType string
	RespBody        []byte
}

// OpenAPISchema represents a bounded OpenAPI 3.0.3 Schema Object.
type OpenAPISchema struct {
	Type       string                    `json:"type,omitempty"`
	Format     string                    `json:"format,omitempty"`
	Nullable   bool                      `json:"nullable,omitempty"`
	Properties map[string]*OpenAPISchema `json:"properties,omitempty"`
	Items      *OpenAPISchema            `json:"items,omitempty"`
}

// OpenAPIDocument is the root OpenAPI 3.0.3 specification emitted by the oracle.
type OpenAPIDocument struct {
	OpenAPI    string                                  `json:"openapi"`
	Info       OpenAPIInfo                             `json:"info"`
	Servers    []OpenAPIServer                         `json:"servers,omitempty"`
	Tags       []OpenAPITag                            `json:"tags,omitempty"`
	Paths      map[string]map[string]*OpenAPIOperation `json:"paths"`
	Components OpenAPIComponents                       `json:"components"`
}

// OpenAPIInfo holds OpenAPI metadata.
type OpenAPIInfo struct {
	Title       string `json:"title"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
}

// OpenAPIServer describes a server base URL.
type OpenAPIServer struct {
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
}

// OpenAPITag describes a route group tag.
type OpenAPITag struct {
	Name string `json:"name"`
}

// OpenAPIComponents holds reusable OpenAPI components such as security schemes.
type OpenAPIComponents struct {
	SecuritySchemes map[string]OpenAPISecurityScheme `json:"securitySchemes,omitempty"`
}

// OpenAPISecurityScheme describes an authentication scheme.
type OpenAPISecurityScheme struct {
	Type         string `json:"type"`
	Scheme       string `json:"scheme,omitempty"`
	BearerFormat string `json:"bearerFormat,omitempty"`
}

// OpenAPIOperation describes a single HTTP operation under a path.
type OpenAPIOperation struct {
	Tags        []string                   `json:"tags,omitempty"`
	Summary     string                     `json:"summary,omitempty"`
	Description string                     `json:"description,omitempty"`
	OperationID string                     `json:"operationId,omitempty"`
	Security    []map[string][]string      `json:"security,omitempty"`
	Parameters  []OpenAPIParameter         `json:"parameters,omitempty"`
	RequestBody *OpenAPIRequestBody        `json:"requestBody,omitempty"`
	Responses   map[string]OpenAPIResponse `json:"responses"`
}

// OpenAPIParameter describes a path or query parameter.
type OpenAPIParameter struct {
	Name        string         `json:"name"`
	In          string         `json:"in"`
	Required    bool           `json:"required"`
	Description string         `json:"description,omitempty"`
	Schema      *OpenAPISchema `json:"schema"`
}

// OpenAPIRequestBody describes a request body payload.
type OpenAPIRequestBody struct {
	Content map[string]OpenAPIMediaType `json:"content"`
}

// OpenAPIResponse describes an HTTP status response.
type OpenAPIResponse struct {
	Description string                      `json:"description"`
	Content     map[string]OpenAPIMediaType `json:"content,omitempty"`
}

// OpenAPIMediaType wraps a schema for a media type.
type OpenAPIMediaType struct {
	Schema *OpenAPISchema `json:"schema"`
}

type statusObservation struct {
	mediaTypes map[string]*OpenAPISchema
}

type pathObservation struct {
	queryParams map[string]bool
	reqSchema   *OpenAPISchema
	responses   map[int]*statusObservation
}

// OpenAPIRecorder accumulates observed TS HTTP traffic and synthesizes an OpenAPI 3.0.3 document.
type OpenAPIRecorder struct {
	mu  sync.Mutex
	obs map[httpCallKey]*pathObservation
}

// NewOpenAPIRecorder creates an empty OpenAPIRecorder.
func NewOpenAPIRecorder() *OpenAPIRecorder {
	return &OpenAPIRecorder{
		obs: make(map[httpCallKey]*pathObservation),
	}
}

// Record folds one observed TS HTTP request/response into the recorder.
func (r *OpenAPIRecorder) Record(obs HTTPObservation) {
	if !obs.IsTS || obs.Status <= 0 {
		return
	}
	method := strings.ToUpper(strings.TrimSpace(obs.Method))
	if method == "" {
		method = http.MethodGet
	}
	key := httpCallKey{method: method, path: normalizeTrackedPath(obs.Path)}

	var reqSchema *OpenAPISchema
	if method == http.MethodPost || method == http.MethodPatch || method == http.MethodPut {
		if isJSONMediaType(parseMediaType(obs.ReqContentType)) && len(obs.ReqBody) > 0 && len(obs.ReqBody) <= maxInferJSONBytes {
			if s, ok := InferJSONSchema(obs.ReqBody); ok {
				reqSchema = s
			}
		}
	}

	respMediaType := parseMediaType(obs.RespContentType)
	var respSchema *OpenAPISchema
	hasRespSchema := false
	if isJSONMediaType(respMediaType) {
		if len(obs.RespBody) > 0 && len(obs.RespBody) <= maxInferJSONBytes {
			if s, ok := InferJSONSchema(obs.RespBody); ok {
				respSchema = s
				hasRespSchema = true
			}
		}
	} else if respMediaType != "" && len(obs.RespBody) > 0 {
		if strings.HasPrefix(respMediaType, "text/") {
			respSchema = &OpenAPISchema{Type: "string"}
		} else {
			respSchema = &OpenAPISchema{Type: "string", Format: "binary"}
		}
		hasRespSchema = true
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	po := r.obs[key]
	if po == nil {
		po = &pathObservation{
			queryParams: make(map[string]bool),
			responses:   make(map[int]*statusObservation),
		}
		r.obs[key] = po
	}
	for qk := range obs.Query {
		if qk != "" {
			po.queryParams[qk] = true
		}
	}
	if reqSchema != nil {
		po.reqSchema = MergeSchema(po.reqSchema, reqSchema)
	}

	st := po.responses[obs.Status]
	if st == nil {
		st = &statusObservation{
			mediaTypes: make(map[string]*OpenAPISchema),
		}
		po.responses[obs.Status] = st
	}
	if hasRespSchema {
		st.mediaTypes[respMediaType] = MergeSchema(st.mediaTypes[respMediaType], respSchema)
	}
}

// BuildDocument matches recorded TS calls against manifest and builds an OpenAPI 3.0.3 spec.
func (r *OpenAPIRecorder) BuildDocument(manifest RouteManifest) OpenAPIDocument {
	r.mu.Lock()
	snap := make(map[httpCallKey]*pathObservation, len(r.obs))
	for k, v := range r.obs {
		snap[k] = v.clone()
	}
	r.mu.Unlock()

	byRoute := make([]*pathObservation, len(manifest.Routes))
	for key, po := range snap {
		idx := matchBestRoute(manifest.Routes, key.method, key.path)
		if idx < 0 {
			continue
		}
		if byRoute[idx] == nil {
			byRoute[idx] = po.clone()
		} else {
			mergePathObservation(byRoute[idx], po)
		}
	}

	paths := make(map[string]map[string]*OpenAPIOperation)
	tagSet := make(map[string]bool)
	usedOpIDs := make(map[string]int)

	for i, route := range manifest.Routes {
		openPath, pathParams := ExpressPathToOpenAPI(route.Path)
		tag := routeTag(route.Path)
		tagSet[tag] = true

		opMethod := strings.ToLower(route.Method)
		if opMethod == "all" {
			opMethod = "get"
		}

		params := make([]OpenAPIParameter, 0, len(pathParams))
		for _, p := range pathParams {
			desc := ""
			if strings.Contains(route.Path, ":"+p+"?") {
				desc = "Optional Express path parameter"
			}
			params = append(params, OpenAPIParameter{
				Name:        p,
				In:          "path",
				Required:    true,
				Description: desc,
				Schema:      &OpenAPISchema{Type: "string"},
			})
		}

		po := byRoute[i]
		if po != nil && len(po.queryParams) > 0 {
			qNames := make([]string, 0, len(po.queryParams))
			for q := range po.queryParams {
				qNames = append(qNames, q)
			}
			slices.Sort(qNames)
			for _, q := range qNames {
				params = append(params, OpenAPIParameter{
					Name:     q,
					In:       "query",
					Required: false,
					Schema:   &OpenAPISchema{Type: "string"},
				})
			}
		}

		var reqBody *OpenAPIRequestBody
		if po != nil && po.reqSchema != nil && (route.Method == http.MethodPost || route.Method == http.MethodPatch || route.Method == http.MethodPut) {
			reqBody = &OpenAPIRequestBody{
				Content: map[string]OpenAPIMediaType{
					"application/json": {Schema: finalizeSchema(po.reqSchema)},
				},
			}
		}

		responses := make(map[string]OpenAPIResponse)
		if po == nil || len(po.responses) == 0 {
			responses["200"] = OpenAPIResponse{Description: "OK"}
		} else {
			for status, st := range po.responses {
				desc := http.StatusText(status)
				if desc == "" {
					desc = fmt.Sprintf("HTTP %d", status)
				}
				resp := OpenAPIResponse{Description: desc}
				if len(st.mediaTypes) > 0 {
					resp.Content = make(map[string]OpenAPIMediaType, len(st.mediaTypes))
					for mt, s := range st.mediaTypes {
						if s != nil {
							resp.Content[mt] = OpenAPIMediaType{Schema: finalizeSchema(s)}
						} else {
							resp.Content[mt] = OpenAPIMediaType{Schema: &OpenAPISchema{}}
						}
					}
				}
				responses[strconv.Itoa(status)] = resp
			}
		}

		var security []map[string][]string
		if route.Auth != "public" {
			security = []map[string][]string{{"bearerAuth": {}}}
		}

		opID := uniqueOperationID(usedOpIDs, route.Method, route.Path)
		op := &OpenAPIOperation{
			Tags:        []string{tag},
			Summary:     route.Method + " " + route.Path,
			Description: routeDescription(route),
			OperationID: opID,
			Security:    security,
			Parameters:  params,
			RequestBody: reqBody,
			Responses:   responses,
		}

		if paths[openPath] == nil {
			paths[openPath] = make(map[string]*OpenAPIOperation)
		}
		paths[openPath][opMethod] = op
	}

	tagNames := make([]string, 0, len(tagSet))
	for name := range tagSet {
		tagNames = append(tagNames, name)
	}
	slices.Sort(tagNames)
	tags := make([]OpenAPITag, 0, len(tagNames))
	for _, name := range tagNames {
		tags = append(tags, OpenAPITag{Name: name})
	}

	return OpenAPIDocument{
		OpenAPI: "3.0.3",
		Info: OpenAPIInfo{
			Title:       "Monstrobook / Audiobookshelf API",
			Version:     "2.37.2",
			Description: "Auto-generated OpenAPI 3.0.3 specification from pinned upstream Audiobookshelf v2.37.2 via the Twin-Server Differential Oracle.",
		},
		Servers: []OpenAPIServer{
			{URL: "/audiobookshelf", Description: "Default router base path"},
		},
		Tags:  tags,
		Paths: paths,
		Components: OpenAPIComponents{
			SecuritySchemes: map[string]OpenAPISecurityScheme{
				"bearerAuth": {
					Type:         "http",
					Scheme:       "bearer",
					BearerFormat: "JWT",
				},
			},
		},
	}
}

// FlushOpenAPI synthesizes the OpenAPI 3.0.3 specification from recorded TS
// traffic and routesPath, and writes formatted JSON to outPath.
func FlushOpenAPI(outPath, routesPath string) error {
	manifest, err := LoadRouteManifest(routesPath)
	if err != nil {
		return err
	}
	doc := surfaceTracker.openapi.BuildDocument(manifest)
	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode openapi document: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return fmt.Errorf("create openapi output dir: %w", err)
	}
	if err := os.WriteFile(outPath, append(encoded, '\n'), 0644); err != nil {
		return fmt.Errorf("write openapi document %s: %w", outPath, err)
	}
	return nil
}

// ExpressPathToOpenAPI converts Express route syntax (:id, :fileid?, *) to
// OpenAPI path templates ({id}, {fileid}, {wildcard}) and returns the ordered
// list of path parameter names.
func ExpressPathToOpenAPI(expressPath string) (string, []string) {
	segs := splitRouteSegments(expressPath)
	if len(segs) == 0 {
		return "/", nil
	}
	outSegs := make([]string, 0, len(segs))
	params := make([]string, 0)
	seen := make(map[string]bool)
	addParam := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			params = append(params, name)
		}
	}

	for _, seg := range segs {
		switch {
		case seg == "*":
			outSegs = append(outSegs, "{wildcard}")
			addParam("wildcard")
		case strings.HasSuffix(seg, "*"):
			prefix := strings.TrimSuffix(seg, "*")
			outSegs = append(outSegs, prefix+"{wildcard}")
			addParam("wildcard")
		case strings.HasPrefix(seg, ":"):
			name := strings.TrimSuffix(strings.TrimPrefix(seg, ":"), "?")
			outSegs = append(outSegs, "{"+name+"}")
			addParam(name)
		default:
			outSegs = append(outSegs, seg)
		}
	}
	return "/" + strings.Join(outSegs, "/"), params
}

// InferJSONSchema parses raw JSON bytes and infers a bounded OpenAPI schema.
func InferJSONSchema(raw []byte) (*OpenAPISchema, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var val any
	if err := dec.Decode(&val); err != nil {
		return nil, false
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, false
	}
	return inferSchemaDepth(val, 0), true
}

// InferSchema infers a bounded OpenAPI schema from an already-decoded JSON value.
func InferSchema(val any) *OpenAPISchema {
	return inferSchemaDepth(val, 0)
}

func inferSchemaDepth(val any, depth int) *OpenAPISchema {
	switch v := val.(type) {
	case nil:
		return &OpenAPISchema{Nullable: true}
	case bool:
		return &OpenAPISchema{Type: "boolean"}
	case string:
		return &OpenAPISchema{Type: "string"}
	case json.Number:
		s := v.String()
		if !strings.ContainsAny(s, ".eE") {
			if _, err := strconv.ParseInt(s, 10, 64); err == nil {
				return &OpenAPISchema{Type: "integer"}
			}
		}
		return &OpenAPISchema{Type: "number"}
	case int, int64, int32:
		return &OpenAPISchema{Type: "integer"}
	case float64:
		if math.Trunc(v) == v && !math.IsInf(v, 0) && !math.IsNaN(v) && v >= math.MinInt64 && v <= math.MaxInt64 {
			return &OpenAPISchema{Type: "integer"}
		}
		return &OpenAPISchema{Type: "number"}
	case []any:
		if depth >= maxSchemaDepth {
			return &OpenAPISchema{Type: "array"}
		}
		var itemSchema *OpenAPISchema
		for _, el := range v {
			itemSchema = mergeSchemaDepth(itemSchema, inferSchemaDepth(el, depth+1), depth+1)
		}
		return &OpenAPISchema{Type: "array", Items: itemSchema}
	case map[string]any:
		if depth >= maxSchemaDepth {
			return &OpenAPISchema{Type: "object"}
		}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		if len(keys) > maxSchemaProperties {
			keys = keys[:maxSchemaProperties]
		}
		var props map[string]*OpenAPISchema
		if len(keys) > 0 {
			props = make(map[string]*OpenAPISchema, len(keys))
			for _, k := range keys {
				props[k] = inferSchemaDepth(v[k], depth+1)
			}
		}
		return &OpenAPISchema{Type: "object", Properties: props}
	default:
		return &OpenAPISchema{}
	}
}

// MergeSchema merges two OpenAPI schemas into a bounded combined schema.
func MergeSchema(a, b *OpenAPISchema) *OpenAPISchema {
	return mergeSchemaDepth(a, b, 0)
}

func mergeSchemaDepth(a, b *OpenAPISchema, depth int) *OpenAPISchema {
	if a == nil {
		return b.clone()
	}
	if b == nil {
		return a.clone()
	}
	if a.Type == "" && a.Nullable {
		if b.Type == "" && b.Nullable {
			return &OpenAPISchema{Nullable: true}
		}
		if b.Type == "" {
			return &OpenAPISchema{}
		}
		res := b.clone()
		res.Nullable = true
		return res
	}
	if b.Type == "" && b.Nullable {
		if a.Type == "" {
			return &OpenAPISchema{}
		}
		res := a.clone()
		res.Nullable = true
		return res
	}
	if a.Type == "" || b.Type == "" {
		return &OpenAPISchema{}
	}

	nullable := a.Nullable || b.Nullable
	if a.Type == b.Type {
		switch a.Type {
		case "object":
			if depth >= maxSchemaDepth {
				return &OpenAPISchema{Type: "object", Nullable: nullable}
			}
			keySet := make(map[string]bool, len(a.Properties)+len(b.Properties))
			for k := range a.Properties {
				keySet[k] = true
			}
			for k := range b.Properties {
				keySet[k] = true
			}
			keys := make([]string, 0, len(keySet))
			for k := range keySet {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			if len(keys) > maxSchemaProperties {
				keys = keys[:maxSchemaProperties]
			}
			var props map[string]*OpenAPISchema
			if len(keys) > 0 {
				props = make(map[string]*OpenAPISchema, len(keys))
				for _, k := range keys {
					props[k] = mergeSchemaDepth(a.Properties[k], b.Properties[k], depth+1)
				}
			}
			return &OpenAPISchema{Type: "object", Nullable: nullable, Properties: props}
		case "array":
			if depth >= maxSchemaDepth {
				return &OpenAPISchema{Type: "array", Nullable: nullable}
			}
			return &OpenAPISchema{
				Type:     "array",
				Nullable: nullable,
				Items:    mergeSchemaDepth(a.Items, b.Items, depth+1),
			}
		default:
			format := a.Format
			if format != b.Format {
				format = ""
			}
			return &OpenAPISchema{Type: a.Type, Format: format, Nullable: nullable}
		}
	}

	if (a.Type == "integer" && b.Type == "number") || (a.Type == "number" && b.Type == "integer") {
		return &OpenAPISchema{Type: "number", Nullable: nullable}
	}
	return &OpenAPISchema{}
}

func (s *OpenAPISchema) clone() *OpenAPISchema {
	if s == nil {
		return nil
	}
	cp := &OpenAPISchema{
		Type:     s.Type,
		Format:   s.Format,
		Nullable: s.Nullable,
	}
	if s.Items != nil {
		cp.Items = s.Items.clone()
	}
	if len(s.Properties) > 0 {
		cp.Properties = make(map[string]*OpenAPISchema, len(s.Properties))
		for k, v := range s.Properties {
			cp.Properties[k] = v.clone()
		}
	}
	return cp
}

func finalizeSchema(s *OpenAPISchema) *OpenAPISchema {
	if s == nil {
		return nil
	}
	cp := s.clone()
	finalizeSchemaInPlace(cp, 0)
	return cp
}

func finalizeSchemaInPlace(s *OpenAPISchema, depth int) {
	if s == nil {
		return
	}
	if s.Type == "" {
		// Null-only or mixed unconstrained schema becomes empty JSON Schema {}.
		s.Nullable = false
		s.Format = ""
		s.Properties = nil
		s.Items = nil
		return
	}
	if depth >= maxSchemaDepth {
		if s.Type == "object" {
			s.Properties = nil
		}
		if s.Type == "array" {
			s.Items = &OpenAPISchema{}
		}
		return
	}
	if s.Type == "array" {
		if s.Items == nil {
			s.Items = &OpenAPISchema{}
		} else {
			finalizeSchemaInPlace(s.Items, depth+1)
		}
	}
	for _, prop := range s.Properties {
		finalizeSchemaInPlace(prop, depth+1)
	}
}

func (po *pathObservation) clone() *pathObservation {
	cp := &pathObservation{
		queryParams: make(map[string]bool, len(po.queryParams)),
		reqSchema:   po.reqSchema.clone(),
		responses:   make(map[int]*statusObservation, len(po.responses)),
	}
	for k, v := range po.queryParams {
		cp.queryParams[k] = v
	}
	for status, st := range po.responses {
		stCopy := &statusObservation{
			mediaTypes: make(map[string]*OpenAPISchema, len(st.mediaTypes)),
		}
		for mt, s := range st.mediaTypes {
			stCopy.mediaTypes[mt] = s.clone()
		}
		cp.responses[status] = stCopy
	}
	return cp
}

func mergePathObservation(dst, src *pathObservation) {
	for q := range src.queryParams {
		dst.queryParams[q] = true
	}
	if src.reqSchema != nil {
		dst.reqSchema = MergeSchema(dst.reqSchema, src.reqSchema)
	}
	for status, srcSt := range src.responses {
		dstSt := dst.responses[status]
		if dstSt == nil {
			dstSt = &statusObservation{
				mediaTypes: make(map[string]*OpenAPISchema, len(srcSt.mediaTypes)),
			}
			dst.responses[status] = dstSt
		}
		for mt, s := range srcSt.mediaTypes {
			dstSt.mediaTypes[mt] = MergeSchema(dstSt.mediaTypes[mt], s)
		}
	}
}

func parseMediaType(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	mt, _, err := mime.ParseMediaType(raw)
	if err != nil {
		base, _, _ := strings.Cut(raw, ";")
		return strings.ToLower(strings.TrimSpace(base))
	}
	return strings.ToLower(strings.TrimSpace(mt))
}

func isJSONMediaType(mt string) bool {
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}

func routeTag(expressPath string) string {
	segs := splitRouteSegments(expressPath)
	if len(segs) >= 2 && segs[0] == "api" {
		return segs[1]
	}
	if len(segs) >= 1 {
		switch segs[0] {
		case "feed":
			return "feeds"
		case "auth":
			return "auth"
		case "public":
			return "public"
		case "hls":
			return "hls"
		}
	}
	return "misc"
}

func routeDescription(route RouteSpec) string {
	var parts []string
	switch route.Auth {
	case "public":
		parts = append(parts, "Public endpoint.")
	case "admin":
		parts = append(parts, "Requires admin authentication.")
	default:
		parts = append(parts, "Requires user authentication.")
	}
	if route.Skip && route.SkipReason != "" {
		parts = append(parts, "Matrix-skipped: "+route.SkipReason+".")
	}
	return strings.Join(parts, " ")
}

func uniqueOperationID(used map[string]int, method, expressPath string) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(method))
	for _, seg := range splitRouteSegments(expressPath) {
		switch {
		case seg == "*":
			b.WriteString("Wildcard")
		case strings.HasSuffix(seg, "*"):
			b.WriteString(toPascalToken(strings.TrimSuffix(seg, "*")))
			b.WriteString("Wildcard")
		case strings.HasPrefix(seg, ":"):
			name := strings.TrimSuffix(strings.TrimPrefix(seg, ":"), "?")
			b.WriteString("By")
			b.WriteString(toPascalToken(name))
		default:
			b.WriteString(toPascalToken(seg))
		}
	}
	base := b.String()
	if base == "" {
		base = "operation"
	}
	count := used[base] + 1
	used[base] = count
	if count == 1 {
		return base
	}
	return base + strconv.Itoa(count)
}

func toPascalToken(raw string) string {
	var b strings.Builder
	upperNext := true
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		isAlnum := (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9')
		if !isAlnum {
			upperNext = true
			continue
		}
		if upperNext && ch >= 'a' && ch <= 'z' {
			b.WriteByte(ch - ('a' - 'A'))
		} else {
			b.WriteByte(ch)
		}
		upperNext = false
	}
	return b.String()
}
