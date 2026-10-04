package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// File is what the rules need to know about one Go source file.
type File struct {
	Rel        string   // slash-separated path relative to the scanned root
	Dir        string   // slash-separated directory relative to the root ("" for the root)
	Test       bool     // *_test.go
	Imports    []string // import paths, any alias, including blank imports
	Constraint string   // the //go:build expression, "" if none
	Sandboxed  bool     // has a "// sandboxed: <reason>" comment with a non-empty reason
	Uses       []Use    // selector expressions of interest (pkg.Name), resolved to import paths
}

// Use is one reference to an identifier of an imported package, e.g. os.StartProcess.
type Use struct {
	Pkg  string // import path ("os", "net", "runtime", ...)
	Name string // selected identifier ("StartProcess", "Listen", "GOOS", ...)
	Arg0 string // first call argument when it is a string literal, else ""
	Line int
}

// Violation is one rule failure, reported as "file:line: rule: detail".
type Violation struct {
	Rel    string
	Line   int
	Rule   string
	Detail string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s:%d: %s: %s", v.Rel, v.Line, v.Rule, v.Detail)
}

// skippedDirs are never scanned: documentation, VCS data, JS/Rust build
// trees and test fixtures (fixtures are scanned explicitly by the tests).
var skippedDirs = map[string]bool{
	".git": true, "docs": true, "node_modules": true, "target": true,
	"dist": true, "vendor": true, "testdata": true, "fixtures": true,
}

// Scan parses every .go file under root. Directories in skippedDirs are not
// entered, except root itself.
func Scan(root string) ([]File, error) {
	var files []File
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && skippedDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		f, err := parseFile(fset, p, filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		files = append(files, f)
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Rel < files[j].Rel })
	return files, err
}

func parseFile(fset *token.FileSet, abs, rel string) (File, error) {
	af, err := parser.ParseFile(fset, abs, nil, parser.ParseComments)
	if err != nil {
		return File{}, fmt.Errorf("parse %s: %w", rel, err)
	}
	f := File{Rel: rel, Dir: path.Dir(rel), Test: strings.HasSuffix(rel, "_test.go")}
	if f.Dir == "." {
		f.Dir = ""
	}
	local := map[string]string{} // local name -> import path
	for _, is := range af.Imports {
		p, _ := strconv.Unquote(is.Path.Value)
		f.Imports = append(f.Imports, p)
		name := path.Base(p)
		if strings.HasPrefix(name, "v") && isDigits(name[1:]) { // .../jsonschema/v6
			name = path.Base(path.Dir(p))
		}
		if is.Name != nil {
			name = is.Name.Name
		}
		local[name] = p
	}
	for _, cg := range af.Comments {
		for _, c := range cg.List {
			if strings.HasPrefix(c.Text, "//go:build ") && c.Pos() < af.Package {
				f.Constraint = strings.TrimSpace(strings.TrimPrefix(c.Text, "//go:build "))
			}
			// gofmt rewrites "//sandboxed:" to "// sandboxed:" in doc comments; accept both.
			text := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
			if reason, ok := strings.CutPrefix(text, "sandboxed:"); ok && strings.TrimSpace(reason) != "" {
				f.Sandboxed = true
			}
		}
	}
	ast.Inspect(af, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			if u, ok := selectorUse(x.Fun, local, fset); ok {
				if len(x.Args) > 0 {
					if bl, ok := x.Args[0].(*ast.BasicLit); ok && bl.Kind == token.STRING {
						u.Arg0, _ = strconv.Unquote(bl.Value)
					}
				}
				f.Uses = append(f.Uses, u)
				// Arguments may contain further uses; keep walking.
				for _, a := range x.Args {
					ast.Inspect(a, func(m ast.Node) bool {
						if se, ok := m.(*ast.SelectorExpr); ok {
							if u, ok := selectorUse(se, local, fset); ok {
								f.Uses = append(f.Uses, u)
							}
						}
						return true
					})
				}
				return false
			}
		case *ast.SelectorExpr:
			if u, ok := selectorUse(x, local, fset); ok {
				f.Uses = append(f.Uses, u)
			}
		}
		return true
	})
	return f, nil
}

func selectorUse(e ast.Expr, local map[string]string, fset *token.FileSet) (Use, bool) {
	se, ok := e.(*ast.SelectorExpr)
	if !ok {
		return Use{}, false
	}
	id, ok := se.X.(*ast.Ident)
	if !ok || id.Obj != nil { // id.Obj != nil: a local variable, not a package
		return Use{}, false
	}
	p, ok := local[id.Name]
	if !ok {
		return Use{}, false
	}
	return Use{Pkg: p, Name: se.Sel.Name, Line: fset.Position(se.Pos()).Line}, true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// under reports whether dir is prefix or inside it.
func under(dir, prefix string) bool {
	return dir == prefix || strings.HasPrefix(dir, prefix+"/")
}

func underAny(dir string, prefixes []string) bool {
	for _, p := range prefixes {
		if under(dir, p) {
			return true
		}
	}
	return false
}

// isStdlib reports whether an import path belongs to the standard library
// (its first element has no dot).
func isStdlib(p string) bool {
	first, _, _ := strings.Cut(p, "/")
	return !strings.Contains(first, ".")
}
