package linux_bwrap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// processStarters are the calls that start a process.
var processStarters = map[string][]string{
	"os":                    {"StartProcess"},
	"syscall":               {"ForkExec", "StartProcess", "Exec"},
	"golang.org/x/sys/unix": {"Exec", "ForkExec"},
}

// TestLauncher_OnlyCommandStartsProcesses keeps the launch in one file
// (M2-13 "must hold": Command must own the systemd-run wrapper). Every
// guarantee about what reaches the sandbox (the empty environment of
// bwrap and of its wrapper, the descriptor plan, close-on-exec of
// inherited descriptors) is made in command.go; a second file that
// imported os/exec or called a process starter could wrap bwrap without
// them. Test files are not counted: the probes there start only fixed
// commands.
func TestLauncher_OnlyCommandStartsProcesses(t *testing.T) {
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly|parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		local := map[string]string{}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if (path == "os/exec" || path == "golang.org/x/sys/execabs") && name != "command.go" {
				t.Errorf("%s imports %s; only command.go may build a launch", name, path)
			}
			alias := filepath.Base(path)
			if imp.Name != nil {
				alias = imp.Name.Name
			}
			local[alias] = path
		}
		full, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(full, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if slices.Contains(processStarters[local[id.Name]], sel.Sel.Name) && name != "command.go" {
				t.Errorf("%s calls %s.%s; only command.go may start a process", name, local[id.Name], sel.Sel.Name)
			}
			return true
		})
	}
}
