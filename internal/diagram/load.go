package diagram

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/build"
	"go/build/constraint"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// Package is one non-test Go package of the module, parsed and type-checked.
type Package struct {
	Path    string // import path
	Name    string // package clause name
	Dir     string // directory relative to the module root ("" for the root package)
	Files   []*ast.File
	Imports []string // module-internal import paths, sorted (non-test files only)
	Types   *types.Package
	Info    *types.Info
}

// Program is the type-checked module.
type Program struct {
	Fset   *token.FileSet
	Module string
	Pkgs   map[string]*Package
}

// PackagePaths returns the module's package paths in sorted order.
func (p *Program) PackagePaths() []string {
	out := make([]string, 0, len(p.Pkgs))
	for k := range p.Pkgs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// IsModulePath reports whether an import path belongs to the module.
func (p *Program) IsModulePath(importPath string) bool {
	return importPath == p.Module || strings.HasPrefix(importPath, p.Module+"/")
}

// The standard library is type-checked from source once per process and shared
// (a stdlib-only setup has no export data to import). Cgo is disabled so
// packages like net type-check in pure Go instead of invoking the cgo tool.
var (
	loadMu  sync.Mutex
	sharedF = token.NewFileSet()
	stdImp  types.Importer
)

func stdImporter() types.Importer {
	if stdImp == nil {
		build.Default.CgoEnabled = false
		stdImp = importer.ForCompiler(sharedF, "source", nil)
	}
	return stdImp
}

// Load parses and type-checks every non-test package under the root of fsys,
// which must be a Go module named module. Test files, testdata, vendor and
// dot/underscore directories are skipped, and files are filtered by build
// constraints for the running GOOS/GOARCH. Any type error is fatal: a
// diagram of code that does not compile would be a lie.
func Load(fsys fs.FS, module string) (*Program, error) {
	loadMu.Lock()
	defer loadMu.Unlock()

	p := &Program{Fset: sharedF, Module: module, Pkgs: map[string]*Package{}}

	err := fs.WalkDir(fsys, ".", func(dir string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		base := path.Base(dir)
		if dir != "." && (strings.HasPrefix(base, ".") || strings.HasPrefix(base, "_") || base == "testdata" || base == "vendor") {
			return fs.SkipDir
		}
		if dir != "." {
			if _, err := fs.Stat(fsys, path.Join(dir, "go.mod")); err == nil {
				return fs.SkipDir // a nested module is not part of this one
			}
		}
		pkg, err := parseDir(p, fsys, dir)
		if err != nil {
			return err
		}
		if pkg != nil {
			p.Pkgs[pkg.Path] = pkg
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(p.Pkgs) == 0 {
		return nil, fmt.Errorf("no Go packages found under module %q", module)
	}

	imp := &modImporter{p: p, std: stdImporter(), state: map[string]int{}}
	for _, ip := range p.PackagePaths() {
		if _, err := imp.Import(ip); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func parseDir(p *Program, fsys fs.FS, dir string) (*Package, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	rel := dir
	if dir == "." {
		rel = ""
	}
	pkg := &Package{Dir: rel, Path: p.Module}
	if rel != "" {
		pkg.Path = p.Module + "/" + rel
	}
	imports := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := fs.ReadFile(fsys, path.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if !matchFile(name, src) {
			continue
		}
		f, err := parser.ParseFile(p.Fset, path.Join(dir, name), src, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		if pkg.Name == "" {
			pkg.Name = f.Name.Name
		} else if pkg.Name != f.Name.Name {
			return nil, fmt.Errorf("%s: found packages %s and %s", dir, pkg.Name, f.Name.Name)
		}
		pkg.Files = append(pkg.Files, f)
		for _, im := range f.Imports {
			ip := strings.Trim(im.Path.Value, `"`)
			if p.IsModulePath(ip) {
				imports[ip] = true
			}
		}
	}
	if len(pkg.Files) == 0 {
		return nil, nil
	}
	for ip := range imports {
		pkg.Imports = append(pkg.Imports, ip)
	}
	sort.Strings(pkg.Imports)
	return pkg, nil
}

// modImporter resolves module-internal imports by type-checking them on
// demand (in dependency order) and defers everything else to the stdlib
// source importer.
type modImporter struct {
	p     *Program
	std   types.Importer
	state map[string]int // 0 unseen, 1 in progress, 2 done
	errs  []error
}

func (m *modImporter) Import(ip string) (*types.Package, error) {
	pkg, ok := m.p.Pkgs[ip]
	if !ok {
		return m.std.Import(ip)
	}
	switch m.state[ip] {
	case 2:
		return pkg.Types, nil
	case 1:
		return nil, fmt.Errorf("import cycle through %s", ip)
	}
	m.state[ip] = 1
	pkg.Info = &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Defs:       map[*ast.Ident]types.Object{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	var terrs []string
	conf := types.Config{
		Importer: m,
		Error: func(err error) {
			if len(terrs) < 5 {
				terrs = append(terrs, err.Error())
			}
		},
	}
	tp, _ := conf.Check(ip, m.p.Fset, pkg.Files, pkg.Info)
	if len(terrs) > 0 {
		return nil, fmt.Errorf("%s does not type-check (fix the build first):\n  %s", ip, strings.Join(terrs, "\n  "))
	}
	pkg.Types = tp
	m.state[ip] = 2
	return tp, nil
}

// --- build constraints ---

var knownOS = map[string]bool{"aix": true, "android": true, "darwin": true, "dragonfly": true, "freebsd": true, "hurd": true, "illumos": true, "ios": true, "js": true, "linux": true, "nacl": true, "netbsd": true, "openbsd": true, "plan9": true, "solaris": true, "wasip1": true, "windows": true, "zos": true}
var knownArch = map[string]bool{"386": true, "amd64": true, "amd64p32": true, "arm": true, "armbe": true, "arm64": true, "arm64be": true, "loong64": true, "mips": true, "mipsle": true, "mips64": true, "mips64le": true, "mips64p32": true, "mips64p32le": true, "ppc": true, "ppc64": true, "ppc64le": true, "riscv": true, "riscv64": true, "s390": true, "s390x": true, "sparc": true, "sparc64": true, "wasm": true}

// matchFile applies the _GOOS/_GOARCH filename rules and any //go:build line.
func matchFile(name string, src []byte) bool {
	stem := strings.TrimSuffix(name, ".go")
	parts := strings.Split(stem, "_")
	if n := len(parts); n >= 2 {
		last := parts[n-1]
		switch {
		case knownArch[last] && n >= 3 && knownOS[parts[n-2]]:
			if parts[n-2] != runtime.GOOS || last != runtime.GOARCH {
				return false
			}
		case knownArch[last]:
			if last != runtime.GOARCH {
				return false
			}
		case knownOS[last]:
			if last != runtime.GOOS {
				return false
			}
		}
	}
	sc := bufio.NewScanner(strings.NewReader(string(src)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "//") && !constraint.IsGoBuild(line) {
			continue
		}
		if constraint.IsGoBuild(line) {
			expr, err := constraint.Parse(line)
			if err != nil {
				return false
			}
			return expr.Eval(func(tag string) bool {
				switch tag {
				case runtime.GOOS, runtime.GOARCH, "gc":
					return true
				case "unix":
					return runtime.GOOS != "windows" && runtime.GOOS != "plan9" && runtime.GOOS != "js" && runtime.GOOS != "wasip1"
				}
				return strings.HasPrefix(tag, "go1.") // release tags: assume the toolchain is recent
			})
		}
		break // first non-comment line ends the header
	}
	return true
}

// --- module discovery ---

// FindModuleRoot walks up from dir to the directory containing go.mod and
// returns it together with the module path.
func FindModuleRoot(dir string) (root, module string, err error) {
	dir, err = filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	for {
		b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				line = strings.TrimSpace(line)
				if rest, ok := strings.CutPrefix(line, "module"); ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t') {
					return dir, strings.Trim(strings.TrimSpace(rest), `"`), nil
				}
			}
			return "", "", fmt.Errorf("%s/go.mod has no module line", dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", fmt.Errorf("no go.mod found in or above the working directory")
		}
		dir = parent
	}
}
