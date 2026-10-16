package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
)

// File is what the rules need to know about one Go source file.
type File struct {
	Rel        string   // slash-separated path relative to the scanned root
	Package    string   // package clause name ("main", "exec", "main_test", ...)
	Dir        string   // slash-separated directory relative to the root ("" for the root)
	Test       bool     // *_test.go
	Imports    []string // import paths, any alias, including blank imports
	DotImports []string // import paths imported with "." (their identifiers are not resolved into Uses)
	Constraint string   // the //go:build expression, "" if none
	Sandboxed  bool     // has a "// sandboxed: <reason>" comment with a non-empty reason
	Linknames  []int    // lines of //go:linkname directives
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

// goIgnored reports whether the go tool ignores a directory or file of this
// name at any depth: testdata, and names starting with "." or "_" (go help
// packages). Such code is never compiled, so it is never scanned, and these
// are the only names Scan skips: any other skip would leave every rule blind
// to code that is compiled into the binaries (round 3 found docs/ at the root
// skipped yet compiled). testdata trees are scanned explicitly by the tests.
func goIgnored(name string) bool {
	return name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

// irregular reports whether a directory entry type is anything but a regular
// file or a directory: a symlink, a Windows junction or other reparse point
// (which Go reports as fs.ModeIrregular), a device, a named pipe or a socket.
func irregular(t fs.FileMode) bool {
	return t != 0 && t != fs.ModeDir
}

// Scan parses every .go file under root that the go tool would compile in
// some build, skipping only what goIgnored names. Build constraints are not
// evaluated. It fails closed: the go tool resolves package directories
// through symlinks and junctions but Scan does not follow them, so every
// irregular entry outside the ignored names is reported in a *ScanError and
// the tree cannot pass until it is removed.
func Scan(root string) ([]File, error) {
	return scanFS(os.DirFS(root))
}

func scanFS(fsys fs.FS) ([]File, error) {
	var files []File
	var refused []Violation
	fset := token.NewFileSet()
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		if goIgnored(d.Name()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if irregular(d.Type()) {
			refused = append(refused, Violation{p, 0, "scan", entryKind(d.Type()) + ": the go tool follows links and archtest does not, so the tree may contain only regular files and directories"})
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") {
			return nil
		}
		src, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		f, err := parseFile(fset, p, src)
		if err != nil {
			return err
		}
		files = append(files, f)
		return nil
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Rel < files[j].Rel })
	if err == nil && len(refused) > 0 {
		err = &ScanError{refused}
	}
	return files, err
}

func parseFile(fset *token.FileSet, rel string, src []byte) (File, error) {
	af, err := parser.ParseFile(fset, rel, src, parser.ParseComments)
	if err != nil {
		return File{}, fmt.Errorf("parse %s: %w", rel, err)
	}
	f := File{Rel: rel, Package: af.Name.Name, Dir: path.Dir(rel), Test: strings.HasSuffix(rel, "_test.go")}
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
		if name == "." {
			f.DotImports = append(f.DotImports, p)
			continue
		}
		local[name] = p
	}
	for _, cg := range af.Comments {
		for _, c := range cg.List {
			if strings.HasPrefix(c.Text, "//go:linkname ") {
				f.Linknames = append(f.Linknames, fset.Position(c.Pos()).Line)
			}
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

// ScanError is returned by Scan when the tree contains entries it cannot
// vouch for: symlinks, junctions and other irregular entries.
type ScanError struct{ Violations []Violation }

func (e *ScanError) Error() string {
	s := make([]string, len(e.Violations))
	for i, v := range e.Violations {
		s[i] = v.String()
	}
	return "archtest scan refused the tree:\n" + strings.Join(s, "\n")
}

func entryKind(t fs.FileMode) string {
	switch {
	case t&fs.ModeSymlink != 0:
		return "symlink"
	case t&fs.ModeIrregular != 0:
		return "irregular entry (a Windows junction or other reparse point)"
	case t&fs.ModeDevice != 0:
		return "device"
	case t&fs.ModeNamedPipe != 0:
		return "named pipe"
	case t&fs.ModeSocket != 0:
		return "socket"
	}
	return "entry of type " + t.String()
}
