// Command exportsurface reports code that the trajector program does not
// need: functions that no path from cmd/ reaches, and exported
// identifiers of internal packages that no other package uses. A use
// from a test counts: the tests exercise packages from outside, and
// what a test checks has to be exported. Run it from the repository
// root:
//
//	exportsurface -deadcode <path>
//
// where <path> is a deadcode binary built from the golang.org/x/tools
// version this module requires. Every finding is printed as
// file:line:col, and the exit code is 1 when there is one.
//
// A finding must hold on every platform that compiles its file: code
// that one platform's files use is not reported for another platform.
// A constant is not reported when another package uses its type or
// another constant of its const block: it is one value of a set, and
// the values of one set are exported together.
//
// A declaration that is kept on purpose carries the directive
//
//	//exportsurface:keep <reason>
//
// in its doc comment. The reason is required, and a directive on a
// declaration with nothing to report is itself reported.
package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"golang.org/x/tools/go/packages"
)

// platforms are the GOOS values the program is built for.
var platforms = []string{"linux", "darwin", "windows"}

// keepDirective marks a declaration whose finding is intended.
const keepDirective = "//exportsurface:keep"

// buildTags are the tags the linters read the code with.
const buildTags = "localcorpus"

// Exported identifiers of these packages exist for tests by design.
const testSupport = "internal/harness/"

func main() {
	deadcode := flag.String("deadcode", "", "path of the deadcode binary")
	flag.Parse()
	if *deadcode == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: exportsurface -deadcode <path>")
		os.Exit(2)
	}
	findings, err := check(context.Background(), *deadcode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "exportsurface: %v\n", err)
		os.Exit(2)
	}
	for _, f := range findings {
		fmt.Println(f)
	}
	if len(findings) > 0 {
		os.Exit(1)
	}
}

// position is a place in the source, with the file relative to the
// repository root, so positions from every source compare equal.
type position struct {
	file      string
	line, col int
}

func (p position) String() string { return fmt.Sprintf("%s:%d:%d", p.file, p.line, p.col) }

// byPosition orders positions by file, then line, then column.
func byPosition(a, b position) int {
	return cmp.Or(strings.Compare(a.file, b.file), cmp.Compare(a.line, b.line), cmp.Compare(a.col, b.col))
}

// view is what one platform's build shows.
type view struct {
	// compiled holds the non-test files the platform compiles.
	compiled map[string]bool
	// dead maps each function deadcode reports to its name.
	dead map[position]string
	// exported maps each exported package-level identifier of an
	// internal package to its qualified name.
	exported map[position]string
	// setOf maps an exported constant to the positions of the set it
	// belongs to: its type, when the same package declares it, and the
	// other constants of its const block.
	setOf map[position][]position
	// used holds the identifiers another package uses.
	used map[position]bool
	// keep maps each declaration that carries the keep directive to
	// its reason.
	keep map[position]string
}

func check(ctx context.Context, deadcode string) ([]string, error) {
	root, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	views := make([]*view, len(platforms))
	errs := make([]error, len(platforms))
	var wg sync.WaitGroup
	for i, goos := range platforms {
		wg.Go(func() {
			views[i], errs[i] = look(ctx, root, goos, deadcode)
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return merge(views), nil
}

// merge keeps a finding only when every platform that compiles its file
// agrees with it.
func merge(views []*view) []string {
	found := map[position]string{}
	for _, v := range views {
		for pos, name := range v.dead {
			if everywhere(views, pos, func(v *view) bool { return v.dead[pos] != "" }) {
				found[pos] = name + ": no path from cmd/ reaches it"
			}
		}
	}
	usedAnywhere := func(pos position) bool {
		return slices.ContainsFunc(views, func(v *view) bool { return v.used[pos] })
	}
	for _, v := range views {
		for pos, name := range v.exported {
			if _, ok := found[pos]; ok || usedAnywhere(pos) {
				continue
			}
			if slices.ContainsFunc(v.setOf[pos], usedAnywhere) {
				continue
			}
			found[pos] = name + ": exported, but no other package uses it"
		}
	}
	for _, v := range views {
		for pos, reason := range v.keep {
			switch _, reported := found[pos]; {
			case reason == "":
				found[pos] = keepDirective + " needs a reason"
			case reported:
				delete(found, pos)
			default:
				found[pos] = keepDirective + ", but nothing here is reported"
			}
		}
	}
	lines := make([]string, 0, len(found))
	for _, pos := range slices.SortedFunc(maps.Keys(found), byPosition) {
		lines = append(lines, pos.String()+": "+found[pos])
	}
	return lines
}

// everywhere reports whether holds is true on every platform that
// compiles the file pos is in.
func everywhere(views []*view, pos position, holds func(*view) bool) bool {
	for _, v := range views {
		if v.compiled[pos.file] && !holds(v) {
			return false
		}
	}
	return true
}

func look(ctx context.Context, root, goos, deadcode string) (*view, error) {
	v := &view{
		compiled: map[string]bool{},
		exported: map[position]string{},
		setOf:    map[position][]position{},
		used:     map[position]bool{},
		keep:     map[position]string{},
	}
	env := append(os.Environ(), "GOOS="+goos)
	var err error
	if v.dead, err = unreachable(ctx, root, env, deadcode); err != nil {
		return nil, fmt.Errorf("%s: deadcode: %w", goos, err)
	}
	pkgs, err := packages.Load(&packages.Config{
		Context:    ctx,
		Mode:       packages.NeedName | packages.NeedModule | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:        root,
		Env:        env,
		BuildFlags: []string{"-tags=" + buildTags},
		Tests:      true,
	}, "./...")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", goos, err)
	}
	if n := packages.PrintErrors(pkgs); n > 0 {
		return nil, fmt.Errorf("%s: %d package errors", goos, n)
	}
	rel := func(p token.Position) position {
		return position{relFile(root, p.Filename), p.Line, p.Column}
	}
	for _, p := range pkgs {
		if p.ID == p.PkgPath {
			for _, f := range p.CompiledGoFiles {
				v.compiled[relFile(root, f)] = true
			}
			for _, file := range p.Syntax {
				for pos, reason := range kept(file) {
					v.keep[rel(p.Fset.Position(pos))] = reason
				}
			}
			if internal(p) {
				v.declare(p, rel)
			}
		}
		mark := func(obj types.Object) {
			if obj != nil && obj.Pkg() != nil && obj.Pkg().Path() != p.PkgPath {
				v.used[rel(p.Fset.Position(obj.Pos()))] = true
			}
		}
		for _, obj := range p.TypesInfo.Uses {
			if obj.Pkg() != nil && obj.Parent() == obj.Pkg().Scope() {
				mark(obj)
			}
		}
		for _, tv := range p.TypesInfo.Types {
			mark(namedIn(tv.Type))
		}
		for _, sel := range p.TypesInfo.Selections {
			mark(namedIn(sel.Recv()))
		}
	}
	return v, nil
}

// declare records the exported package-level identifiers of p and the
// value set of each exported constant.
func (v *view) declare(p *packages.Package, rel func(token.Position) position) {
	for _, name := range p.Types.Scope().Names() {
		obj := p.Types.Scope().Lookup(name)
		if !obj.Exported() {
			continue
		}
		pos := rel(p.Fset.Position(obj.Pos()))
		v.exported[pos] = p.Name + "." + name
		if _, ok := obj.(*types.Const); !ok {
			continue
		}
		if typ := namedIn(obj.Type()); typ != nil && typ.Pkg() == p.Types {
			v.setOf[pos] = append(v.setOf[pos], rel(p.Fset.Position(typ.Pos())))
		}
	}
	for _, file := range p.Syntax {
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST || !gen.Lparen.IsValid() {
				continue
			}
			var block []position
			for _, spec := range gen.Specs {
				for _, name := range spec.(*ast.ValueSpec).Names {
					block = append(block, rel(p.Fset.Position(name.Pos())))
				}
			}
			for _, pos := range block {
				v.setOf[pos] = append(v.setOf[pos], block...)
			}
		}
	}
}

// unreachable runs deadcode on the commands under cmd/.
func unreachable(ctx context.Context, root string, env []string, deadcode string) (map[position]string, error) {
	cmd := exec.CommandContext(ctx, deadcode, "-json", "./cmd/...")
	cmd.Dir, cmd.Env = root, env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w\n%s", err, stderr.String())
	}
	var report []struct {
		Name  string
		Funcs []struct {
			Name     string
			Position struct {
				File      string
				Line, Col int
			}
			Generated bool
		}
	}
	if err := json.Unmarshal(out, &report); err != nil {
		return nil, err
	}
	dead := map[position]string{}
	for _, p := range report {
		for _, f := range p.Funcs {
			if f.Generated {
				continue
			}
			dead[position{relFile(root, f.Position.File), f.Position.Line, f.Position.Col}] = p.Name + "." + f.Name
		}
	}
	return dead, nil
}

// kept maps the name of every declaration in file that carries the keep
// directive to the directive's reason.
func kept(file *ast.File) map[token.Pos]string {
	found := map[token.Pos]string{}
	note := func(doc *ast.CommentGroup, names ...*ast.Ident) {
		if doc == nil {
			return
		}
		for _, c := range doc.List {
			if rest, ok := strings.CutPrefix(c.Text, keepDirective); ok && (rest == "" || rest[0] == ' ') {
				for _, name := range names {
					found[name.Pos()] = strings.TrimSpace(rest)
				}
			}
		}
	}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			note(d.Doc, d.Name)
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					note(d.Doc, s.Name)
					note(s.Doc, s.Name)
				case *ast.ValueSpec:
					note(d.Doc, s.Names...)
					note(s.Doc, s.Names...)
				}
			}
		}
	}
	return found
}

// relFile is file relative to root, with forward slashes.
func relFile(root, file string) string {
	if filepath.IsAbs(file) {
		if r, err := filepath.Rel(root, file); err == nil {
			file = r
		}
	}
	return filepath.ToSlash(file)
}

// namedIn is the declared type a value of type t carries, looking
// through pointers and containers.
func namedIn(t types.Type) types.Object {
	for t != nil {
		switch u := types.Unalias(t).(type) {
		case *types.Named:
			return u.Origin().Obj()
		case *types.Pointer:
			t = u.Elem()
		case *types.Slice:
			t = u.Elem()
		case *types.Array:
			t = u.Elem()
		case *types.Map:
			t = u.Elem()
		case *types.Chan:
			t = u.Elem()
		default:
			return nil
		}
	}
	return nil
}

// internal reports whether p is an internal package of the main module
// that is not test support.
func internal(p *packages.Package) bool {
	if p.Module == nil || !p.Module.Main {
		return false
	}
	rest, ok := strings.CutPrefix(p.PkgPath, p.Module.Path+"/")
	return ok && strings.HasPrefix(rest, "internal/") && !strings.HasPrefix(rest+"/", testSupport)
}
