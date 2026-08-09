package proxytest_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/PublicAI01/trajector-cli/internal/harness/repotest"
)

// Test servers listen on ephemeral ports, so a pooled connection held by
// the process-wide client can resurface at an address a later test's
// server owns and fail with an EOF unrelated to that test. Every request
// a test sends itself must ride a client scoped to one test: Client, an
// Env helper, or a fake server's own client.
func TestTestRequestsNeverRideTheProcessWideConnectionPool(t *testing.T) {
	// Patterns are assembled at run time so this table is not a finding.
	shared := []string{"DefaultClient", "Get(", "Post(", "PostForm(", "Head("}
	for i, name := range shared {
		shared[i] = "http." + name
	}
	handRolled := "http." + "Client{"

	var hits []string
	repotest.Lines(t, func(l repotest.Line) {
		if !l.File.Test() && !strings.HasPrefix(l.File.Rel, "internal/harness/") {
			return
		}
		banned := shared
		if l.File.Test() {
			// Hand-rolling a client in a test either shares the
			// process-wide transport or leaves a pool nothing drops
			// with the test; only the harness builds clients.
			banned = append(append([]string(nil), shared...), handRolled)
		}
		for _, pattern := range banned {
			if strings.Contains(l.Text, pattern) {
				hits = append(hits, l.String())
			}
		}
	})
	if len(hits) > 0 {
		t.Errorf("requests must go through a client scoped to the test (proxytest.Client, an Env helper, or a fake server's client):\n%s",
			strings.Join(hits, "\n"))
	}
}

// A client built without a transport of its own borrows the
// process-wide pool: connections it opens outlive the exchange and are
// shared with every other component in the process, so a request made
// after the address changed hands can be handed a connection to a
// server that is gone. Every client this module builds names its own
// transport.
func TestEveryHTTPClientCarriesItsOwnConnectionPool(t *testing.T) {
	fset := token.NewFileSet()
	var hits []string
	for _, f := range repotest.GoFiles(t) {
		parsed, err := parser.ParseFile(fset, f.Path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isHTTPClient(lit.Type) || namesField(lit, "Transport") {
				return true
			}
			hits = append(hits, fmt.Sprintf("%s:%d", f.Rel, fset.Position(lit.Pos()).Line))
			return true
		})
	}
	if len(hits) > 0 {
		t.Errorf("every HTTP client must name a transport of its own, or it rides the process-wide connection pool:\n%s",
			strings.Join(hits, "\n"))
	}
}

func isHTTPClient(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Client" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "http"
}

func namesField(lit *ast.CompositeLit, field string) bool {
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if key, ok := kv.Key.(*ast.Ident); ok && key.Name == field {
			return true
		}
	}
	return false
}
