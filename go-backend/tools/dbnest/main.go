// Command dbnest finds database calls made on the pool while a *sql.Rows
// cursor or a *sql.Tx from the same *sql.DB is still open.
//
// Production opens SQLite with ONE connection (store.Open calls
// SetMaxOpenConns(1)). A query issued on the pool while a cursor or a
// transaction holds that connection waits for it forever, so the request hangs
// until it is cancelled. This checker is syntactic (go/ast, no type
// information) and conservative; it is a guard rail, not a proof.
//
// What it flags, inside internal/**/*.go (test files are skipped unless -tests):
//
//   - a pool call (Query, QueryRow, Exec, Begin, ... on a receiver named db,
//     *DB, database, or h.store.DB()) or a call to a same-package function or
//     method that itself reaches the pool, made after `rows, err := X.Query(...)`
//     and before the cursor is closed or provably exhausted;
//   - the same calls made after `tx, err := db.BeginTx(...)` and before the
//     transaction is committed.
//
// Passing a pool-like expression (db, h.db) to a call counts as a pool call,
// because the callee can query it. A false positive is silenced with a
// `//dbnest:ok <reason>` comment on the call line or the line above.
//
// Usage: go run ./tools/dbnest [-tests] [dir]   (default dir: internal)
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const modulePrefix = "audiobookshelf-compat/internal/"

var poolMethods = map[string]bool{
	"Query": true, "QueryContext": true, "QueryRow": true, "QueryRowContext": true,
	"Exec": true, "ExecContext": true, "Begin": true, "BeginTx": true,
	"Prepare": true, "PrepareContext": true,
}

// sqlResultMethods are database/sql method names; a same-package method with
// one of these names must not make calls on *sql.Rows or *sql.Tx look like pool
// queries.
var sqlResultMethods = map[string]bool{
	"Scan": true, "Next": true, "Close": true, "Err": true, "Columns": true,
	"Commit": true, "Rollback": true,
}

var cursorMethods = map[string]bool{"Query": true, "QueryContext": true}

var txMethods = map[string]bool{"Begin": true, "BeginTx": true}

var verbose bool

type fileInfo struct {
	name    string
	file    *ast.File
	imports map[string]string // local name -> package key (internal packages only)
}

type pkg struct {
	key     string
	files   []*fileInfo
	funcs   map[string][]*ast.FuncDecl // plain functions by name
	methods map[string][]*ast.FuncDecl // methods by name (receiver type ignored)
}

type finding struct {
	pos token.Position
	msg string
}

type analysis struct {
	fset      *token.FileSet
	pkgs      map[string]*pkg
	queriers  map[string]bool // "pkg.Func" and "pkg#Method" reaching the pool
	findings  []finding
	allowed   map[string]map[int]bool // file -> lines carrying //dbnest:ok
	enclosing map[*ast.FuncDecl]string
}

func main() {
	tests := flag.Bool("tests", false, "also analyse _test.go files")
	flag.BoolVar(&verbose, "v", false, "print unresolved calls made inside open regions (manual review aid)")
	flag.Parse()
	dir := "internal"
	if flag.NArg() > 0 {
		dir = flag.Arg(0)
	}
	findings, err := run(dir, *tests)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	for _, f := range findings {
		fmt.Fprintf(os.Stderr, "%s:%d: %s\n", f.pos.Filename, f.pos.Line, f.msg)
	}
	if len(findings) > 0 {
		fmt.Fprintf(os.Stderr, "dbnest: %d nested database call(s) on the one-connection pool; materialize rows first or use the tx\n", len(findings))
		os.Exit(1)
	}
}

func run(dir string, tests bool) ([]finding, error) {
	a := &analysis{
		fset:      token.NewFileSet(),
		pkgs:      map[string]*pkg{},
		queriers:  map[string]bool{},
		allowed:   map[string]map[int]bool{},
		enclosing: map[*ast.FuncDecl]string{},
	}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, err := parser.ParseFile(a.fset, path, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		for _, cg := range file.Comments {
			for _, c := range cg.List {
				if strings.Contains(c.Text, "dbnest:ok") {
					line := a.fset.Position(c.Pos()).Line
					if a.allowed[path] == nil {
						a.allowed[path] = map[int]bool{}
					}
					a.allowed[path][line] = true
					a.allowed[path][line+1] = true
				}
			}
		}
		a.checkPoolSize(path, file)
		if strings.HasSuffix(path, "_test.go") && !tests {
			return nil
		}
		key := filepath.ToSlash(filepath.Dir(path))
		key = strings.TrimPrefix(key, "internal/")
		p := a.pkgs[key]
		if p == nil {
			p = &pkg{key: key, funcs: map[string][]*ast.FuncDecl{}, methods: map[string][]*ast.FuncDecl{}}
			a.pkgs[key] = p
		}
		fi := &fileInfo{name: path, file: file, imports: map[string]string{}}
		for _, imp := range file.Imports {
			ipath := strings.Trim(imp.Path.Value, `"`)
			if !strings.HasPrefix(ipath, modulePrefix) {
				continue
			}
			rest := strings.TrimPrefix(ipath, modulePrefix)
			local := filepath.Base(rest)
			if imp.Name != nil {
				local = imp.Name.Name
			}
			fi.imports[local] = rest
		}
		p.files = append(p.files, fi)
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			if fd.Recv != nil {
				p.methods[fd.Name.Name] = append(p.methods[fd.Name.Name], fd)
			} else {
				p.funcs[fd.Name.Name] = append(p.funcs[fd.Name.Name], fd)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	a.computeQueriers()
	for _, key := range sortedKeys(a.pkgs) {
		p := a.pkgs[key]
		for _, fi := range p.files {
			a.checkFile(p, fi)
		}
	}
	sort.Slice(a.findings, func(i, j int) bool {
		if a.findings[i].pos.Filename != a.findings[j].pos.Filename {
			return a.findings[i].pos.Filename < a.findings[j].pos.Filename
		}
		return a.findings[i].pos.Line < a.findings[j].pos.Line
	})
	return a.findings, nil
}

// checkPoolSize requires every function that opens SQLite to also call
// SetMaxOpenConns(1), tests included: a fixture with a bigger pool hides the
// nested-query deadlocks production hits.
func (a *analysis) checkPoolSize(path string, file *ast.File) {
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		var opens []*ast.CallExpr
		limited := false
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "sql" && sel.Sel.Name == "Open" && len(call.Args) == 2 {
				if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Value == `"sqlite"` {
					opens = append(opens, call)
				}
			}
			if sel.Sel.Name == "SetMaxOpenConns" && len(call.Args) == 1 {
				if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Value == "1" {
					limited = true
				}
			}
			return true
		})
		if limited {
			continue
		}
		for _, call := range opens {
			pos := a.fset.Position(call.Pos())
			if a.allowed[path][pos.Line] {
				continue
			}
			a.findings = append(a.findings, finding{pos, "sql.Open(\"sqlite\") without SetMaxOpenConns(1) in " + fd.Name.Name + ": production uses one connection, so the pool must too"})
		}
	}
}

func sortedKeys(m map[string]*pkg) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// isPoolExpr reports whether e names a *sql.DB: an identifier or selector
// whose last element is db/DB/database (or ends in "db"/"DB"), or a call to a
// method named DB.
func isPoolExpr(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.Ident:
		return poolName(x.Name)
	case *ast.SelectorExpr:
		return poolName(x.Sel.Name)
	case *ast.CallExpr:
		if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
			return sel.Sel.Name == "DB"
		}
	case *ast.ParenExpr:
		return isPoolExpr(x.X)
	case *ast.StarExpr:
		return isPoolExpr(x.X)
	}
	return false
}

func poolName(name string) bool {
	n := strings.ToLower(name)
	return n == "db" || n == "database" || strings.HasSuffix(n, "db") && !strings.HasPrefix(n, "tx")
}

// poolUse describes how a call touches the pool, or "" when it does not.
func (a *analysis) poolUse(p *pkg, fi *fileInfo, call *ast.CallExpr) string {
	if sel, ok := call.Fun.(*ast.SelectorExpr); ok && poolMethods[sel.Sel.Name] && isPoolExpr(sel.X) {
		return exprString(sel.X) + "." + sel.Sel.Name
	}
	for _, arg := range call.Args {
		if isPoolExpr(arg) {
			return "passes " + exprString(arg) + " to " + callName(call)
		}
	}
	return ""
}

func callName(call *ast.CallExpr) string {
	switch f := call.Fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return exprString(f)
	}
	return "call"
}

func exprString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return exprString(x.X) + "." + x.Sel.Name
	case *ast.CallExpr:
		return exprString(x.Fun) + "()"
	case *ast.ParenExpr:
		return exprString(x.X)
	case *ast.StarExpr:
		return "*" + exprString(x.X)
	case *ast.IndexExpr:
		return exprString(x.X) + "[]"
	}
	return "?"
}

// callee resolves a call to the key of a same-module function/method, if any.
func (a *analysis) callees(p *pkg, fi *fileInfo, call *ast.CallExpr) []string {
	switch f := call.Fun.(type) {
	case *ast.Ident:
		if _, ok := p.funcs[f.Name]; ok {
			return []string{p.key + "." + f.Name}
		}
	case *ast.SelectorExpr:
		if id, ok := f.X.(*ast.Ident); ok {
			if target, ok := fi.imports[id.Name]; ok {
				return []string{target + "." + f.Sel.Name}
			}
		}
		if sqlResultMethods[f.Sel.Name] {
			return nil
		}
		if _, ok := p.methods[f.Sel.Name]; ok {
			return []string{p.key + "#" + f.Sel.Name}
		}
	}
	return nil
}

func (a *analysis) computeQueriers() {
	type unit struct {
		key  string
		p    *pkg
		fi   *fileInfo
		body *ast.BlockStmt
	}
	var units []unit
	for _, p := range a.pkgs {
		for _, fi := range p.files {
			for _, decl := range fi.file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				key := p.key + "." + fd.Name.Name
				if fd.Recv != nil {
					key = p.key + "#" + fd.Name.Name
				}
				units = append(units, unit{key, p, fi, fd.Body})
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, u := range units {
			if a.queriers[u.key] {
				continue
			}
			hit := false
			ast.Inspect(u.body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || hit {
					return !hit
				}
				if a.poolUse(u.p, u.fi, call) != "" {
					hit = true
					return false
				}
				for _, c := range a.callees(u.p, u.fi, call) {
					if a.queriers[c] {
						hit = true
						return false
					}
				}
				return true
			})
			if hit {
				a.queriers[u.key] = true
				changed = true
			}
		}
	}
}

func (a *analysis) checkFile(p *pkg, fi *fileInfo) {
	ast.Inspect(fi.file, func(n ast.Node) bool {
		var list []ast.Stmt
		switch b := n.(type) {
		case *ast.BlockStmt:
			list = b.List
		case *ast.CaseClause:
			list = b.Body
		case *ast.CommClause:
			list = b.Body
		default:
			return true
		}
		for i, stmt := range list {
			if name, call := openedBy(stmt); call != nil {
				sel := call.Fun.(*ast.SelectorExpr)
				if len(call.Args) == 0 {
					continue // url.Values-style Query(), not a SQL call
				}
				switch {
				case cursorMethods[sel.Sel.Name]:
					a.checkCursor(p, fi, list, i, name, stmt, call)
				case txMethods[sel.Sel.Name] && isPoolExpr(sel.X):
					a.checkTx(p, fi, list, i, name, stmt)
				}
			}
		}
		return true
	})
}

// openedBy returns the first assigned name and the call for statements of the
// form `x, err := recv.Method(...)`, `x, err = ...` and `var x, err = ...`.
func openedBy(stmt ast.Stmt) (string, *ast.CallExpr) {
	var lhs []ast.Expr
	var rhs []ast.Expr
	switch s := stmt.(type) {
	case *ast.AssignStmt:
		lhs, rhs = s.Lhs, s.Rhs
	case *ast.DeclStmt:
		gd, ok := s.Decl.(*ast.GenDecl)
		if !ok || len(gd.Specs) != 1 {
			return "", nil
		}
		vs, ok := gd.Specs[0].(*ast.ValueSpec)
		if !ok {
			return "", nil
		}
		for _, id := range vs.Names {
			lhs = append(lhs, id)
		}
		rhs = vs.Values
	default:
		return "", nil
	}
	if len(rhs) != 1 || len(lhs) == 0 {
		return "", nil
	}
	call, ok := rhs[0].(*ast.CallExpr)
	if !ok {
		return "", nil
	}
	if _, ok := call.Fun.(*ast.SelectorExpr); !ok {
		return "", nil
	}
	id, ok := lhs[0].(*ast.Ident)
	if !ok || id.Name == "_" {
		return "", nil
	}
	return id.Name, call
}

func (a *analysis) checkCursor(p *pkg, fi *fileInfo, list []ast.Stmt, i int, rows string, open ast.Stmt, opener *ast.CallExpr) {
	end := len(list) // exclusive
	for j := i + 1; j < len(list); j++ {
		if closesRows(list[j], rows) {
			end = j
			break
		}
		if exhaustsRows(list[j], rows) {
			// Next() returning false closes the cursor, so the region ends with the loop.
			end = j + 1
			break
		}
	}
	line := a.fset.Position(open.Pos()).Line
	for j := i + 1; j < end && j < len(list); j++ {
		a.scan(p, fi, list[j], fmt.Sprintf("while cursor %q (opened line %d) is open", rows, line), "")
	}
}

func (a *analysis) checkTx(p *pkg, fi *fileInfo, list []ast.Stmt, i int, tx string, open ast.Stmt) {
	end := len(list)
	for j := i + 1; j < len(list); j++ {
		if commits(list[j], tx) {
			end = j
			break
		}
	}
	line := a.fset.Position(open.Pos()).Line
	for j := i + 1; j < end; j++ {
		a.scan(p, fi, list[j], fmt.Sprintf("while transaction %q (opened line %d) is open", tx, line), tx)
	}
}

func (a *analysis) scan(p *pkg, fi *fileInfo, stmt ast.Stmt, why, tx string) {
	ast.Inspect(stmt, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		desc := a.poolUse(p, fi, call)
		if desc == "" {
			for _, c := range a.callees(p, fi, call) {
				if a.queriers[c] {
					desc = "call to " + callName(call) + " (reaches the pool)"
					break
				}
			}
		}
		if desc == "" {
			if verbose {
				a.note(p, fi, call, why)
			}
			return true
		}
		pos := a.fset.Position(call.Pos())
		if a.allowed[pos.Filename][pos.Line] {
			return true
		}
		a.findings = append(a.findings, finding{pos, desc + " " + why})
		return true
	})
}

// closesRows reports whether stmt (not a deferred call, not inside a closure)
// calls rows.Close().
func closesRows(stmt ast.Stmt, rows string) bool {
	if _, ok := stmt.(*ast.DeferStmt); ok {
		return false
	}
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit, *ast.DeferStmt:
			return false
		case *ast.CallExpr:
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Close" {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == rows {
					found = true
				}
			}
			// An immediately invoked closure runs its deferred Close before it returns.
			if lit, ok := x.Fun.(*ast.FuncLit); ok && closesInside(lit.Body, rows) {
				found = true
			}
		}
		return !found
	})
	return found
}

func closesInside(body *ast.BlockStmt, rows string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Close" {
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == rows {
				found = true
			}
		}
		return !found
	})
	return found
}

func commits(stmt ast.Stmt, tx string) bool {
	if _, ok := stmt.(*ast.DeferStmt); ok {
		return false
	}
	found := false
	ast.Inspect(stmt, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit, *ast.DeferStmt:
			return false
		case *ast.CallExpr:
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Commit" {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == tx {
					found = true
				}
			}
		}
		return !found
	})
	return found
}

// exhaustsRows reports whether stmt is `for rows.Next() {...}` without a
// break, or `if err == nil { ... for rows.Next() {...} ... }` (the loop runs
// whenever the cursor was opened).
func exhaustsRows(stmt ast.Stmt, rows string) bool {
	switch s := stmt.(type) {
	case *ast.ForStmt:
		return loopOverRows(s, rows) && !hasBreak(s.Body)
	case *ast.IfStmt:
		cond, ok := s.Cond.(*ast.BinaryExpr)
		if !ok || cond.Op != token.EQL || s.Else != nil {
			return false
		}
		if id, ok := cond.Y.(*ast.Ident); !ok || id.Name != "nil" {
			return false
		}
		for _, inner := range s.Body.List {
			if exhaustsRows(inner, rows) {
				return true
			}
		}
	}
	return false
}

func loopOverRows(loop *ast.ForStmt, rows string) bool {
	call, ok := loop.Cond.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Next" {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == rows
}

func hasBreak(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.BranchStmt:
			if x.Tok == token.BREAK || x.Tok == token.GOTO {
				found = true
			}
		}
		return !found
	})
	return found
}

// note prints calls the checker cannot resolve (methods on fields of other
// packages, function-valued fields) so a reviewer can look at them.
func (a *analysis) note(p *pkg, fi *fileInfo, call *ast.CallExpr, why string) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	if id, ok := sel.X.(*ast.Ident); ok {
		if _, isPkg := fi.imports[id.Name]; isPkg || id.Name == "rows" || id.Name == "tx" || id.Name == "r" || id.Name == "w" || id.Name == "err" {
			return
		}
	}
	if _, inPkg := p.methods[sel.Sel.Name]; inPkg {
		return
	}
	switch sel.X.(type) {
	case *ast.SelectorExpr, *ast.Ident:
		pos := a.fset.Position(call.Pos())
		fmt.Fprintf(os.Stderr, "note %s:%d: unresolved %s %s\n", pos.Filename, pos.Line, exprString(sel), why)
	}
}
