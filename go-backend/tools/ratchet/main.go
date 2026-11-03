// Command ratchet counts architecture and parity-harness metrics and prevents
// them from increasing above the checked-in baseline.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type metrics map[string]int

func main() {
	update := flag.Bool("update", false, "write current counts to ratchet-baseline.json")
	top := flag.Int("top", 0, "also list the N most complex functions with file:line")
	flag.Parse()
	if *top < 0 {
		fatalIf(fmt.Errorf("-top must be 0 or more, got %d", *top))
	}
	root, err := repositoryRoot()
	fatalIf(err)
	counts, funcs, err := measure(root)
	fatalIf(err)
	path := filepath.Join(root, "go-backend", "ratchet-baseline.json")
	data, err := os.ReadFile(path)
	fatalIf(err)
	var baseline metrics
	fatalIf(json.Unmarshal(data, &baseline))
	if *update {
		updated, err := lowerBaseline(counts, baseline)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		data, err = json.MarshalIndent(updated, "", "  ")
		fatalIf(err)
		fatalIf(os.WriteFile(path, append(data, '\n'), 0o644))
		fmt.Printf("updated %s\n", path)
		return
	}
	for _, name := range sortedMetricNames(counts) {
		if old, ok := baseline[name]; ok {
			fmt.Printf("%s: %d (baseline %d, delta %+d)\n", name, counts[name], old, counts[name]-old)
		}
	}
	if *top > 0 {
		printTop(os.Stdout, funcs, *top)
	}
	problems := compareCounts(counts, baseline)
	for _, problem := range problems {
		fmt.Fprintln(os.Stderr, problem)
	}
	if len(problems) > 0 {
		os.Exit(1)
	}
}

func sortedMetricNames(values metrics) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func compareCounts(counts, baseline metrics) []string {
	var problems []string
	for _, name := range sortedMetricNames(counts) {
		old, ok := baseline[name]
		if !ok {
			problems = append(problems, fmt.Sprintf("missing baseline metric %q", name))
			continue
		}
		switch {
		case counts[name] > old:
			problems = append(problems, fmt.Sprintf("%s: %d exceeds baseline %d", name, counts[name], old))
		case counts[name] < old:
			problems = append(problems, fmt.Sprintf("%s: %d is below baseline %d; run `make ratchet-update` and commit", name, counts[name], old))
		}
	}
	for _, name := range sortedMetricNames(baseline) {
		if _, ok := counts[name]; !ok {
			problems = append(problems, fmt.Sprintf("baseline metric %q is no longer measured", name))
		}
	}
	return problems
}

func lowerBaseline(counts, baseline metrics) (metrics, error) {
	for _, name := range sortedMetricNames(baseline) {
		old := baseline[name]
		now, ok := counts[name]
		if !ok {
			return nil, fmt.Errorf("refusing to update: baseline metric %q is no longer measured", name)
		}
		if now > old {
			return nil, fmt.Errorf("refusing to raise %s baseline: current %d, baseline %d", name, now, old)
		}
	}
	return counts, nil
}

func isHandleBoolRouter(function *ast.FuncDecl) bool {
	if function.Recv == nil || function.Name.Name != "Handle" || function.Type.Results == nil || len(function.Type.Results.List) != 1 {
		return false
	}
	result, ok := function.Type.Results.List[0].Type.(*ast.Ident)
	return ok && result.Name == "bool"
}

func repositoryRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go-backend", "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not locate repository root from %s", wd)
		}
	}
}

func measure(root string) (metrics, []funcStat, error) {
	goRoot := filepath.Join(root, "go-backend")
	goCounts, err := measureGo(goRoot)
	if err != nil {
		return nil, nil, err
	}
	simplification, funcs, err := measureSimplification(goRoot)
	if err != nil {
		return nil, nil, err
	}
	for key, value := range simplification {
		goCounts[key] = value
	}
	harness, err := measureHarness(filepath.Join(root, "go-backend", "tools", "parity"))
	if err != nil {
		return nil, nil, err
	}
	for key, value := range harness {
		goCounts[key] = value
	}
	for _, key := range []string{
		"map[string]any and aliases", "[]any", "hand-rolled Handle bool routers", "writeJSON-style copies",
		"EmitFunc-style declarations", "Set* injector methods", "catalog.New call sites",
		"httptest imports in non-test files", "inline comparePair calls", "freePort helper definitions",
		"login helper definitions", "request helper definitions", "waitReady helper definitions",
		"parseArgs helper definitions",
	} {
		if _, exists := goCounts[key]; !exists {
			goCounts[key] = 0
		}
	}
	return goCounts, funcs, nil
}

// complexityThreshold is the per-function cyclomatic complexity above which a
// function is counted by the "functions with complexity > 15" metric.
const complexityThreshold = 15

// funcStat is one FuncDecl with its cyclomatic complexity, for -top.
type funcStat struct {
	name       string
	file       string
	line       int
	complexity int
}

// measureSimplification counts handwritten lines and cyclomatic complexity over
// non-test, non-generated .go files under internal and cmd. Complexity is the
// gocyclo definition: 1 per FuncDecl plus one per if, for, range, non-default
// case or select comm clause, and per && and ||. FuncLits add to their enclosing
// FuncDecl.
func measureSimplification(root string) (metrics, []funcStat, error) {
	counts := metrics{
		"handwritten Go lines":           0,
		"cyclomatic complexity total":    0,
		"functions with complexity > 15": 0,
	}
	var funcs []funcStat
	fset := token.NewFileSet()
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if isGeneratedSource(src) {
				return nil
			}
			file, err := parser.ParseFile(fset, path, src, parser.AllErrors)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			counts["handwritten Go lines"] += physicalLines(src)
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				c := complexity(fn)
				counts["cyclomatic complexity total"] += c
				if c > complexityThreshold {
					counts["functions with complexity > 15"]++
				}
				funcs = append(funcs, funcStat{
					name:       funcName(fn),
					file:       rel,
					line:       fset.Position(fn.Pos()).Line,
					complexity: c,
				})
			}
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	return counts, funcs, nil
}

var generatedHeader = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// isGeneratedSource reports whether a line before the package clause matches
// the generated-code header.
func isGeneratedSource(src []byte) bool {
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "package ") {
			return false
		}
		if generatedHeader.MatchString(line) {
			return true
		}
	}
	return false
}

// physicalLines counts lines as an editor does: a final line without a newline
// still counts.
func physicalLines(src []byte) int {
	n := bytes.Count(src, []byte("\n"))
	if len(src) > 0 && src[len(src)-1] != '\n' {
		n++
	}
	return n
}

// complexity returns the gocyclo complexity of one FuncDecl, including the
// bodies of any FuncLits it contains.
func complexity(fn *ast.FuncDecl) int {
	total := 1
	ast.Inspect(fn, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt:
			total++
		case *ast.CaseClause:
			if n.List != nil {
				total++
			}
		case *ast.CommClause:
			if n.Comm != nil {
				total++
			}
		case *ast.BinaryExpr:
			if n.Op == token.LAND || n.Op == token.LOR {
				total++
			}
		}
		return true
	})
	return total
}

// funcName returns "Name" for functions and "Recv.Name" for methods.
func funcName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	if recv := receiverName(fn.Recv.List[0].Type); recv != "" {
		return recv + "." + fn.Name.Name
	}
	return fn.Name.Name
}

func receiverName(expr ast.Expr) string {
	switch n := expr.(type) {
	case *ast.StarExpr:
		return receiverName(n.X)
	case *ast.ParenExpr:
		return receiverName(n.X)
	case *ast.IndexExpr:
		return receiverName(n.X)
	case *ast.IndexListExpr:
		return receiverName(n.X)
	case *ast.Ident:
		return n.Name
	}
	return ""
}

// printTop lists the n most complex functions, most complex first.
func printTop(w io.Writer, funcs []funcStat, n int) {
	sorted := append([]funcStat(nil), funcs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.complexity != b.complexity {
			return a.complexity > b.complexity
		}
		if a.file != b.file {
			return a.file < b.file
		}
		return a.line < b.line
	})
	if n > len(sorted) {
		n = len(sorted)
	}
	fmt.Fprintf(w, "top %d most complex functions:\n", n)
	for _, f := range sorted[:n] {
		fmt.Fprintf(w, "%4d  %s:%d  %s\n", f.complexity, f.file, f.line, f.name)
	}
}

func measureGo(root string) (metrics, error) {
	counts := metrics{}
	fset := token.NewFileSet()
	var files []sourceFile
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.AllErrors)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, sourceFile{path: filepath.ToSlash(rel), file: file})
		return nil
	})
	if err != nil {
		return nil, err
	}
	counts["map[string]any and aliases"], counts["[]any"] = countDynamicTypes("audiobookshelf-compat", files)
	for _, file := range files {
		isTest := strings.HasSuffix(file.path, "_test.go")
		ast.Inspect(file.file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.FuncDecl:
				if isHandleBoolRouter(n) {
					counts["hand-rolled Handle bool routers"]++
				}
				if strings.Contains(strings.ToLower(n.Name.Name), "writejson") {
					counts["writeJSON-style copies"]++
				}
				if n.Recv != nil && strings.HasPrefix(n.Name.Name, "Set") {
					counts["Set* injector methods"]++
				}
			case *ast.TypeSpec:
				if strings.HasSuffix(n.Name.Name, "EmitFunc") {
					counts["EmitFunc-style declarations"]++
				}
			case *ast.CallExpr:
				if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "New" {
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "catalog" {
						counts["catalog.New call sites"]++
					}
				}
			case *ast.ImportSpec:
				if !isTest && n.Path.Value == `"net/http/httptest"` {
					counts["httptest imports in non-test files"]++
				}
			}
			return true
		})
	}
	return counts, nil
}

// The dynamic-map metric counts type declarations and type uses whose resolved
// representation is map[string]any. An alias declaration and each typed use
// both count; ordinary value identifiers do not. Package-qualified aliases
// are keyed by import path, so same-named types in unrelated packages cannot
// collide.
type sourceFile struct {
	path string
	file *ast.File
}

func countDynamicMapTypes(module string, files []sourceFile) int {
	maps, _ := countDynamicTypes(module, files)
	return maps
}

// countDynamicTypes returns the dynamic-map count described above and the
// number of []any slice types (including slices of aliases of any), counted at
// every syntactic position like the direct maps.
func countDynamicTypes(module string, files []sourceFile) (int, int) {
	packagePath := func(file sourceFile) string {
		dir := filepath.ToSlash(filepath.Dir(file.path))
		if dir == "." {
			return module
		}
		return module + "/" + dir
	}
	packageNames := map[string]string{}
	for _, file := range files {
		packageNames[packagePath(file)] = file.file.Name.Name
	}
	aliases := map[string]bool{}
	anyAliases := map[string]bool{}
	importsByFile := make(map[string]map[string]string, len(files))
	for _, file := range files {
		imports := map[string]string{}
		for _, spec := range file.file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				continue
			}
			name := packageNames[path]
			if spec.Name != nil {
				name = spec.Name.Name
			}
			if name != "" && name != "_" && name != "." {
				imports[name] = path
			}
		}
		importsByFile[file.path] = imports
	}
	// Resolve aliases of predeclared any separately: a scalar alias such as
	// `type Value = any` is not itself a dynamic-map alias.
	for changed := true; changed; {
		changed = false
		for _, file := range files {
			pkg := packagePath(file)
			for _, decl := range file.file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.TYPE {
					continue
				}
				for _, spec := range gen.Specs {
					ts := spec.(*ast.TypeSpec)
					key := pkg + "." + ts.Name.Name
					if isAnyType(ts.Type, pkg, importsByFile[file.path], anyAliases) && !anyAliases[key] {
						anyAliases[key] = true
						changed = true
					}
				}
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, file := range files {
			pkg := packagePath(file)
			for _, decl := range file.file.Decls {
				gen, ok := decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.TYPE {
					continue
				}
				for _, spec := range gen.Specs {
					ts := spec.(*ast.TypeSpec)
					key := pkg + "." + ts.Name.Name
					if typeIsDynamicMap(ts.Type, pkg, importsByFile[file.path], aliases, anyAliases) && !aliases[key] {
						aliases[key] = true
						changed = true
					}
				}
			}
		}
	}
	count, slices := 0, 0
	for _, file := range files {
		pkg, imports := packagePath(file), importsByFile[file.path]
		// Preserve the historical direct-map count across every syntactic
		// context, then add alias references at type positions.
		ast.Inspect(file.file, func(node ast.Node) bool {
			if mt, ok := node.(*ast.MapType); ok && isDirectDynamicMap(mt, pkg, imports, anyAliases) {
				count++
			}
			if at, ok := node.(*ast.ArrayType); ok && at.Len == nil && isAnyType(at.Elt, pkg, imports, anyAliases) {
				slices++
			}
			switch n := node.(type) {
			case *ast.GenDecl:
				for _, spec := range n.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						count += countAliasRefs(s.Type, pkg, imports, aliases)
					case *ast.ValueSpec:
						if s.Type != nil {
							count += countAliasRefs(s.Type, pkg, imports, aliases)
						}
					}
				}
			case *ast.FuncDecl:
				count += countAliasRefs(n.Type, pkg, imports, aliases)
			case *ast.FuncLit:
				count += countAliasRefs(n.Type, pkg, imports, aliases)
			case *ast.CompositeLit:
				count += countAliasRefs(n.Type, pkg, imports, aliases)
			case *ast.CallExpr:
				count += countAliasRefs(n.Fun, pkg, imports, aliases)
			case *ast.TypeAssertExpr:
				if n.Type != nil {
					count += countAliasRefs(n.Type, pkg, imports, aliases)
				}
			}
			return true
		})
	}
	return count, slices
}

func isAnyType(expr ast.Expr, pkg string, imports map[string]string, aliases map[string]bool) bool {
	switch n := expr.(type) {
	case *ast.Ident:
		return n.Name == "any" || aliases[pkg+"."+n.Name]
	case *ast.SelectorExpr:
		id, ok := n.X.(*ast.Ident)
		return ok && aliases[imports[id.Name]+"."+n.Sel.Name]
	case *ast.InterfaceType:
		return n.Methods == nil || len(n.Methods.List) == 0
	}
	return false
}

func typeIsDynamicMap(expr ast.Expr, pkg string, imports map[string]string, aliases, anyAliases map[string]bool) bool {
	switch n := expr.(type) {
	case *ast.MapType:
		key, ok := n.Key.(*ast.Ident)
		return ok && key.Name == "string" && isAnyType(n.Value, pkg, imports, anyAliases)
	case *ast.Ident:
		return aliases[pkg+"."+n.Name]
	case *ast.SelectorExpr:
		id, ok := n.X.(*ast.Ident)
		return ok && aliases[imports[id.Name]+"."+n.Sel.Name]
	}
	return false
}

func isDirectDynamicMap(n *ast.MapType, pkg string, imports map[string]string, anyAliases map[string]bool) bool {
	key, ok := n.Key.(*ast.Ident)
	return ok && key.Name == "string" && isAnyType(n.Value, pkg, imports, anyAliases)
}

func countAliasRefs(expr ast.Expr, pkg string, imports map[string]string, aliases map[string]bool) int {
	count := 0
	switch n := expr.(type) {
	case *ast.Ident:
		if aliases[pkg+"."+n.Name] {
			return 1
		}
	case *ast.SelectorExpr:
		if id, ok := n.X.(*ast.Ident); ok && aliases[imports[id.Name]+"."+n.Sel.Name] {
			return 1
		}
	}
	walk := func(child ast.Expr) {
		if child != nil {
			count += countAliasRefs(child, pkg, imports, aliases)
		}
	}
	switch n := expr.(type) {
	case *ast.ArrayType:
		walk(n.Len)
		walk(n.Elt)
	case *ast.ChanType:
		walk(n.Value)
	case *ast.Ellipsis:
		walk(n.Elt)
	case *ast.FuncType:
		for _, list := range []*ast.FieldList{n.Params, n.Results} {
			if list != nil {
				for _, field := range list.List {
					walk(field.Type)
				}
			}
		}
	case *ast.InterfaceType:
		if n.Methods != nil {
			for _, field := range n.Methods.List {
				walk(field.Type)
			}
		}
	case *ast.MapType:
		walk(n.Key)
		walk(n.Value)
	case *ast.ParenExpr:
		walk(n.X)
	case *ast.SelectorExpr:
		walk(n.X)
	case *ast.StarExpr:
		walk(n.X)
	case *ast.StructType:
		if n.Fields != nil {
			for _, field := range n.Fields.List {
				walk(field.Type)
			}
		}
	case *ast.TypeAssertExpr:
		walk(n.Type)
	case *ast.UnaryExpr:
		walk(n.X)
	}
	return count
}

func measureHarness(root string) (metrics, error) {
	counts := metrics{}
	compareRE := regexp.MustCompile(`\bcomparePair\s*\(`)
	files, err := filepath.Glob(filepath.Join(root, "differential.cjs"))
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		counts["inline comparePair calls"] = len(compareRE.FindAll(data, -1))
	}
	files = nil
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".cjs") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	patterns := map[string]*regexp.Regexp{
		"freePort helper definitions":  regexp.MustCompile(`(?m)\bfunction\s+freePort\s*\(|\b(?:const|let|var)\s+freePort\s*=\s*(?:async\s*)?(?:function\b|\()`),
		"login helper definitions":     regexp.MustCompile(`(?m)\bfunction\s+login\s*\(|\b(?:const|let|var)\s+login\s*=\s*(?:async\s*)?(?:function\b|\()`),
		"request helper definitions":   regexp.MustCompile(`(?m)\bfunction\s+(?:request|requestJSON)\s*\(|\b(?:const|let|var)\s+(?:request|requestJSON)\s*=\s*(?:async\s*)?(?:function\b|\()`),
		"waitReady helper definitions": regexp.MustCompile(`(?m)\bfunction\s+waitReady\s*\(|\b(?:const|let|var)\s+waitReady\s*=\s*(?:async\s*)?(?:function\b|\()`),
		"parseArgs helper definitions": regexp.MustCompile(`(?m)\bfunction\s+parseArgs\s*\(|\b(?:const|let|var)\s+parseArgs\s*=\s*(?:async\s*)?(?:function\b|\()`),
	}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		for name, re := range patterns {
			counts[name] += len(re.FindAll(data, -1))
		}
	}
	return counts, nil
}

func fatalIf(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
