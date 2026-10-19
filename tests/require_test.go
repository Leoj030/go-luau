package tests

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Leoj030/go-luau/luau"
)

type reqEnv struct {
	L     *luau.State
	files map[string]string
}

func newReqEnv(t *testing.T, files map[string]string) *reqEnv {
	t.Helper()
	fsys := fstest.MapFS{}
	for name, src := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(src)}
	}
	L := newState(t)
	if err := L.OpenRequire(luau.FSRequirer(fsys)); err != nil {
		t.Fatal(err)
	}
	return &reqEnv{L: L, files: files}
}

// run executes one of the files as a script, under the chunk name the
// requirer expects, and returns what it returns.
func (e *reqEnv) run(t *testing.T, name string) ([]any, error) {
	t.Helper()
	bc, err := luau.Compile(e.files[name])
	if err != nil {
		t.Fatalf("compile %s: %v", name, err)
	}
	if err := e.L.Load(bc, "@"+name); err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return e.L.Call()
}

func TestRequireRelativePaths(t *testing.T) {
	e := newReqEnv(t, map[string]string{
		"main.luau":     `return require("./lib/util").double(21)`,
		"lib/util.luau": `return {double = function(x) return x * 2 end}`,
	})
	res, err := e.run(t, "main.luau")
	if err != nil || len(res) != 1 || res[0] != 42.0 {
		t.Errorf("got %v, %v; want [42]", res, err)
	}
}

func TestRequireSiblingsAndParents(t *testing.T) {
	e := newReqEnv(t, map[string]string{
		"main.luau":  `return require("./lib/a")`,
		"lib/a.luau": `return require("./b") .. require("../top")`,
		"lib/b.luau": `return "b"`,
		"top.luau":   `return "top"`,
	})
	res, err := e.run(t, "main.luau")
	if err != nil || len(res) != 1 || res[0] != "btop" {
		t.Errorf("got %v, %v; want [btop]", res, err)
	}
}

func TestRequireInitFiles(t *testing.T) {
	e := newReqEnv(t, map[string]string{
		"main.luau":     `return require("./pkg") .. require("./pkg/sub")`,
		"pkg/init.luau": `return "pkg:"`,
		"pkg/sub.luau":  `return "sub"`,
	})
	res, err := e.run(t, "main.luau")
	if err != nil || len(res) != 1 || res[0] != "pkg:sub" {
		t.Errorf("got %v, %v; want [pkg:sub]", res, err)
	}
}

func TestRequireFromDoString(t *testing.T) {
	e := newReqEnv(t, map[string]string{"lib/util.luau": `return {n = 7}`})
	if err := e.L.DoString(`result = require("./lib/util").n`); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.L.GetGlobal("result"); got != 7.0 {
		t.Errorf("got %v, want 7", got)
	}
}

func TestRequireCachesModules(t *testing.T) {
	e := newReqEnv(t, map[string]string{
		"main.luau": `return require("./m") == require("./m")`,
		"m.luau":    `return {}`,
	})
	res, err := e.run(t, "main.luau")
	if err != nil || len(res) != 1 || res[0] != true {
		t.Errorf("got %v, %v; want the same table both times", res, err)
	}
}

func TestRequireAliases(t *testing.T) {
	e := newReqEnv(t, map[string]string{
		".luaurc":       `{"aliases": {"lib": "./lib"}}`,
		"main.luau":     `return require("@lib/util")`,
		"lib/util.luau": `return "from alias"`,
	})
	res, err := e.run(t, "main.luau")
	if err != nil || len(res) != 1 || res[0] != "from alias" {
		t.Errorf("got %v, %v; want [from alias]", res, err)
	}
}

func TestRegisterModule(t *testing.T) {
	L := newState(t)
	if err := L.OpenRequire(luau.FSRequirer(fstest.MapFS{})); err != nil {
		t.Fatal(err)
	}
	if err := L.Push("preserved"); err != nil {
		t.Fatal(err)
	}
	if err := L.RegisterModule("@std/json", map[string]luau.Func{
		"encode": func(c *luau.State) (int, error) {
			value, err := c.Get(1)
			if err != nil {
				return 0, err
			}
			text, ok := value.(string)
			if !ok {
				return 0, errors.New("encode: expected a string")
			}
			if err := c.Push("encoded:" + text); err != nil {
				return 0, err
			}
			return 1, nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := L.Get(-1); err != nil || got != "preserved" {
		t.Fatalf("stack value after module registration = %v, %v; want preserved", got, err)
	}
	if err := L.Pop(1); err != nil {
		t.Fatal(err)
	}

	if err := L.DoString(`
		local json = require("@std/json")
		result = json.encode("value")
		sameModule = json == require("@STD/JSON")
	`); err != nil {
		t.Fatal(err)
	}
	if got, _ := L.GetGlobal("result"); got != "encoded:value" {
		t.Errorf("encode result = %v, want %q", got, "encoded:value")
	}
	if got, _ := L.GetGlobal("sameModule"); got != true {
		t.Errorf("case-insensitive module registration = %v, want true", got)
	}
	if L.Top() != 0 {
		t.Errorf("stack not clean: %d", L.Top())
	}
}

func TestRegisterModuleValidation(t *testing.T) {
	L := newState(t)
	if err := L.Push("preserved"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path  string
		funcs map[string]luau.Func
	}{
		{"std/json", nil},
		{"@std/json", map[string]luau.Func{"nil": nil}},
		{"@std/json", map[string]luau.Func{"bad\x00name": func(*luau.State) (int, error) { return 0, nil }}},
	} {
		if err := L.RegisterModule(tc.path, tc.funcs); err == nil {
			t.Errorf("RegisterModule(%q, %v) succeeded, want an error", tc.path, tc.funcs)
		}
		if L.Top() != 1 {
			t.Errorf("stack height after invalid registration = %d, want 1", L.Top())
		}
	}
}

func TestRequireErrors(t *testing.T) {
	for _, c := range []struct {
		name  string
		files map[string]string
		want  string // text the error must contain; "" accepts any error
	}{
		{"not found", map[string]string{"main.luau": `return require("./nope")`}, ""},
		{"escaping the root", map[string]string{"main.luau": `return require("../secret")`}, ""},
		{"ambiguous", map[string]string{
			"main.luau":   `return require("./x")`,
			"x.luau":      `return 1`,
			"x/init.luau": `return 2`,
		}, ""},
		{"module errors", map[string]string{
			"main.luau": `return require("./m")`,
			"m.luau":    `error("boom")`,
		}, "boom"},
		{"no result", map[string]string{
			"main.luau": `return require("./m")`,
			"m.luau":    `local x = 1`,
		}, "single value"},
		{"yielding module", map[string]string{
			"main.luau": `return require("./m")`,
			"m.luau":    `coroutine.yield()`,
		}, "yield"},
		{"syntax error", map[string]string{
			"main.luau": `return require("./m")`,
			"m.luau":    `local = =`,
		}, "Expected identifier"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newReqEnv(t, c.files)
			_, err := e.run(t, "main.luau")
			if err == nil {
				t.Fatal("expected an error")
			}
			if c.want != "" && !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v, want it to contain %q", err, c.want)
			}
			t.Logf("error: %v", err)
			if e.L.Top() != 0 {
				t.Errorf("stack not clean: %d", e.L.Top())
			}
		})
	}
}

// brokenSource wraps a real requirer but fails when asked for code.
type brokenSource struct {
	luau.Requirer
	panics bool
}

func (b brokenSource) Source(string) ([]byte, error) {
	if b.panics {
		panic("oops")
	}
	return nil, errors.New("disk on fire")
}

func TestRequirerErrorsAndPanicsBecomeLuauErrors(t *testing.T) {
	fsys := fstest.MapFS{
		"main.luau": &fstest.MapFile{Data: []byte(`return require("./m")`)},
		"m.luau":    &fstest.MapFile{Data: []byte(`return 1`)},
	}
	for name, want := range map[string]string{"error": "disk on fire", "panic": "oops"} {
		t.Run(name, func(t *testing.T) {
			L := newState(t)
			r := brokenSource{Requirer: luau.FSRequirer(fsys), panics: name == "panic"}
			if err := L.OpenRequire(r); err != nil {
				t.Fatal(err)
			}
			bc, _ := luau.Compile(`return require("./m")`)
			L.Load(bc, "@main.luau")
			_, err := L.Call()
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("got %v, want an error containing %q", err, want)
			}
			if err := L.DoString(`x = 1`); err != nil {
				t.Errorf("state unusable afterwards: %v", err)
			}
		})
	}
}

func TestOpenRequireMisuse(t *testing.T) {
	L := newState(t)
	if err := L.OpenRequire(nil); err == nil {
		t.Error("a nil Requirer should fail")
	}
	th, _ := L.NewThread()
	defer th.Close()
	if err := th.OpenRequire(luau.FSRequirer(fstest.MapFS{})); err == nil {
		t.Error("OpenRequire on a thread should fail")
	}
	if err := L.OpenRequire(luau.FSRequirer(fstest.MapFS{})); err != nil {
		t.Fatal(err)
	}
	if err := L.OpenRequire(luau.FSRequirer(fstest.MapFS{})); err == nil {
		t.Error("opening require twice should fail")
	}
}
