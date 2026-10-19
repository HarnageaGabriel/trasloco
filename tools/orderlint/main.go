// Command orderlint reports direct iteration over maps in ordering-sensitive packages.
package main

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/singlechecker"
)

const directive = "//trasloco:unordered-ok"

// Analyzer flags map ranges unless an adjacent directive explains why order is irrelevant.
var Analyzer = &analysis.Analyzer{
	Name: "orderlint",
	Doc:  "reject range over maps in ordering-sensitive packages",
	Run:  run,
}

func main() { singlechecker.Main(Analyzer) }

func run(pass *analysis.Pass) (any, error) {
	if !targetPackage(pass.Pkg.Path()) {
		return nil, nil
	}
	for _, file := range pass.Files {
		exemptLines := exemptions(pass, file)
		ast.Inspect(file, func(node ast.Node) bool {
			statement, ok := node.(*ast.RangeStmt)
			if !ok || !isMap(pass.TypesInfo.TypeOf(statement.X)) {
				return true
			}
			line := pass.Fset.Position(statement.For).Line
			if exemptLines[line] || exemptLines[line-1] {
				return true
			}
			pass.Reportf(statement.For, "range over map is nondeterministic; sort keys or add %s <reason>", directive)
			return true
		})
	}
	return nil, nil
}

func targetPackage(path string) bool {
	if strings.Contains(path, "/dialect/") || strings.HasSuffix(path, "/dialect") {
		return true
	}
	for _, name := range []string{"ir", "event", "report", "plan", "naming"} {
		if strings.HasSuffix(path, "/"+name) || path == name {
			return true
		}
	}
	return false
}

func isMap(typ types.Type) bool {
	if typ == nil {
		return false
	}
	_, ok := typ.Underlying().(*types.Map)
	return ok
}

func exemptions(pass *analysis.Pass, file *ast.File) map[int]bool {
	lines := make(map[int]bool)
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if !strings.HasPrefix(comment.Text, directive) {
				continue
			}
			reason := strings.TrimSpace(strings.TrimPrefix(comment.Text, directive))
			if reason != "" {
				lines[pass.Fset.Position(comment.Slash).Line] = true
			}
		}
	}
	return lines
}
