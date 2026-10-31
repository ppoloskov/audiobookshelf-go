package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

func TestDynamicMapMetricCountsAliasDeclarationsAndUses(t *testing.T) {
	files := fixtureFiles(t, map[string]string{
		"internal/items/items.go": "package items\ntype Object = map[string]any\ntype Chained = Object\nvar First Object\nvar Second Chained\nfunc Use(value Chained) map[string]any { return map[string]any{} }\n",
	})
	if got, want := countDynamicMapTypes("audiobookshelf-compat", files), 7; got != want {
		t.Fatalf("dynamic map declaration/use count = %d, want %d", got, want)
	}
}

func TestDynamicMapMetricResolvesImportedAliasByPackageIdentity(t *testing.T) {
	files := fixtureFiles(t, map[string]string{
		"internal/items/items.go":   "package items\ntype Object = map[string]any\n",
		"internal/first/first.go":   "package first\nimport data \"audiobookshelf-compat/internal/items\"\nvar Value data.Object\n",
		"internal/second/second.go": "package second\nimport data \"audiobookshelf-compat/internal/items\"\nvar Value data.Object\n",
		"internal/other/other.go":   "package other\ntype Object = string\nvar Value Object\n",
	})
	if got, want := countDynamicMapTypes("audiobookshelf-compat", files), 3; got != want {
		t.Fatalf("dynamic map declaration/use count = %d, want %d", got, want)
	}
}

func fixtureFiles(t *testing.T, sources map[string]string) []sourceFile {
	t.Helper()
	var files []sourceFile
	for name, src := range sources {
		f, err := parser.ParseFile(token.NewFileSet(), name, src, parser.AllErrors)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, sourceFile{path: filepath.Clean(name), file: f})
	}
	return files
}

func TestDynamicMapMetricDoesNotCountScalarAnyAliases(t *testing.T) {
	files := fixtureFiles(t, map[string]string{
		"internal/items/items.go": "package items\ntype Value = any\ntype Text = string\nvar First Value\nvar Second Text\n",
	})
	if got := countDynamicMapTypes("audiobookshelf-compat", files); got != 0 {
		t.Fatalf("scalar aliases counted as dynamic maps: %d", got)
	}
}

func TestDynamicMapMetricCountsEquivalentMapSpellings(t *testing.T) {
	files := fixtureFiles(t, map[string]string{
		"internal/items/items.go": "package items\ntype Any = any\ntype Object = map[string]interface{}\ntype AliasObject = map[string]Any\nvar First Object\nvar Second AliasObject\nvar Third map[string]any\n",
	})
	if got, want := countDynamicMapTypes("audiobookshelf-compat", files), 5; got != want {
		t.Fatalf("dynamic map declaration/use count = %d, want %d", got, want)
	}
}

func TestCompareCountsRequiresEqualityAndHintsAtLowerBaseline(t *testing.T) {
	baseline := metrics{"equal": 5, "lower": 5, "higher": 5}
	if problems := compareCounts(metrics{"equal": 5}, metrics{"equal": 5}); len(problems) != 0 {
		t.Fatalf("equal counts produced problems: %v", problems)
	}
	problems := compareCounts(metrics{"equal": 5, "lower": 4, "higher": 6}, baseline)
	if len(problems) != 2 {
		t.Fatalf("compareCounts() = %v, want two problems", problems)
	}
	joined := strings.Join(problems, "\n")
	if !strings.Contains(joined, "lower: 4 is below baseline 5; run `make ratchet-update` and commit") {
		t.Fatalf("lowered metric problem = %q", joined)
	}
	if !strings.Contains(joined, "higher: 6 exceeds baseline 5") {
		t.Fatalf("raised metric problem = %q", joined)
	}
}

func TestLowerBaselineAcceptsLowerEqualToRejectsRaiseAndMissing(t *testing.T) {
	baseline := metrics{"lower": 5, "equal": 2}
	updated, err := lowerBaseline(metrics{"lower": 4, "equal": 2}, baseline)
	if err != nil {
		t.Fatalf("lowerBaseline() error = %v", err)
	}
	if updated["lower"] != 4 || updated["equal"] != 2 {
		t.Fatalf("lowerBaseline() = %#v", updated)
	}
	if _, err := lowerBaseline(metrics{"lower": 6, "equal": 2}, baseline); err == nil {
		t.Fatal("lowerBaseline() accepted a raised metric")
	}
	if _, err := lowerBaseline(metrics{"lower": 4}, baseline); err == nil {
		t.Fatal("lowerBaseline() accepted a missing metric")
	}
}

func TestHandleRouterDetectorHandlesMethodsWithoutResults(t *testing.T) {
	source := `package fixture
type T struct{}
func (x T) Handle(w, r) {}
type U struct{}
func (x U) Handle(w, r) bool { return false }
`
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", source, parser.AllErrors)
	if err != nil {
		t.Fatal(err)
	}
	var noResult, boolResult *ast.FuncDecl
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "Handle" {
			continue
		}
		receiver := function.Recv.List[0].Type.(*ast.Ident).Name
		switch receiver {
		case "T":
			noResult = function
		case "U":
			boolResult = function
		}
	}
	if noResult == nil || boolResult == nil {
		t.Fatalf("fixture methods not found: noResult=%v boolResult=%v", noResult, boolResult)
	}
	if isHandleBoolRouter(noResult) {
		t.Fatal("a Handle method without results was counted as a bool router")
	}
	if !isHandleBoolRouter(boolResult) {
		t.Fatal("a Handle method returning bool was not counted")
	}
}
