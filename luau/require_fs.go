package luau

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

const (
	jsonConfigName = ".luaurc"
	luauConfigName = ".config.luau"
)

var (
	fileSuffixes = []string{".luau", ".lua"}
	initNames    = []string{"init.luau", "init.lua"}
)

// FSRequirer returns a Requirer that serves modules from fsys, following
// Luau's own rules: the module "lib/util" is the file lib/util.luau or
// lib/util.lua, or lib/util/init.luau or lib/util/init.lua. A directory with
// none of those is a plain directory. If more than one of them exists, the
// module is ambiguous and requiring it fails. Aliases come from .luaurc
// (JSON) and .config.luau files. Luau runs the latter when it finds one.
//
// A script finds its own place in fsys through its chunk name: "@dir/x.luau"
// must name a file that exists in fsys, so that "./y" resolves beside it. A
// chunk named "=something", such as the one DoString uses, resolves as if it
// were a file in the root. Paths can never leave the root of fsys.
//
// Aliases whose path is absolute are not supported, since nothing outside
// fsys is reachable. os.DirFS follows symbolic links, so a link inside the
// directory can still point outside it. Use a file system that doesn't
// follow them if that matters to you.
//
// The returned Requirer keeps a cursor, so use one per State.
func FSRequirer(fsys fs.FS) Requirer { return &fsRequirer{fsys: fsys} }

var _ Requirer = (*fsRequirer)(nil)

type fsRequirer struct {
	fsys       fs.FS
	anonymous  bool   // the requirer is a chunk without a file, such as "=dostring"
	modulePath string // a path in fsys without a suffix; "" is the root
	realPath   string // what modulePath resolves to: a file, a directory, or "." for the root
}

func (r *fsRequirer) isFile(p string) bool {
	if !fs.ValidPath(p) {
		return false
	}
	st, err := fs.Stat(r.fsys, p)
	return err == nil && st.Mode().IsRegular()
}

func (r *fsRequirer) isDir(p string) bool {
	if p == "" {
		p = "."
	}
	if !fs.ValidPath(p) {
		return false
	}
	st, err := fs.Stat(r.fsys, p)
	return err == nil && st.IsDir()
}

// resolve finds what modulePath refers to. Two matches are ambiguous.
func (r *fsRequirer) resolve() (string, NavResult) {
	p := r.modulePath
	found := false
	real := ""

	if p != "" && path.Base(p) != "init" {
		for _, suffix := range fileSuffixes {
			if r.isFile(p + suffix) {
				if found {
					return "", NavAmbiguous
				}
				real, found = p+suffix, true
			}
		}
	}
	if r.isDir(p) {
		if found {
			return "", NavAmbiguous
		}
		real = p
		if real == "" {
			real = "."
		}
		for _, name := range initNames {
			if r.isFile(path.Join(p, name)) {
				if found {
					return "", NavAmbiguous
				}
				real, found = path.Join(p, name), true
			}
		}
		found = true
	}
	if !found {
		return "", NavNotFound
	}
	return real, NavSuccess
}

// update resolves modulePath, and remembers the result if it worked.
func (r *fsRequirer) update() NavResult {
	real, status := r.resolve()
	if status == NavSuccess {
		r.realPath = real
	}
	return status
}

// moduleFromFile turns the path of a script file ("a/b/foo.luau") into the
// path of the module it defines ("a/b/foo"). It reports false for paths
// that fsys can't contain.
func moduleFromFile(p string) (string, bool) {
	p = path.Clean(strings.ReplaceAll(p, "\\", "/"))
	if !fs.ValidPath(p) || p == "." {
		return "", false
	}
	if base := path.Base(p); base == "init.luau" || base == "init.lua" {
		dir := path.Dir(p)
		if dir == "." {
			return "", true
		}
		return dir, true
	}
	for _, suffix := range fileSuffixes {
		if strings.HasSuffix(p, suffix) {
			return strings.TrimSuffix(p, suffix), true
		}
	}
	return p, true
}

func validName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\")
}

func (r *fsRequirer) AllowRequire(chunkname string) bool {
	return strings.HasPrefix(chunkname, "@") || strings.HasPrefix(chunkname, "=")
}

func (r *fsRequirer) Reset(chunkname string) NavResult {
	r.anonymous = false
	switch {
	case strings.HasPrefix(chunkname, "@"):
		p, ok := moduleFromFile(chunkname[1:])
		if !ok {
			return NavNotFound
		}
		r.modulePath = p
		return r.update()
	case strings.HasPrefix(chunkname, "="):
		r.anonymous = true
		r.modulePath, r.realPath = "", "."
		return NavSuccess
	}
	return NavNotFound
}

// JumpToAlias isn't supported: it is only used for paths outside the file
// system's configuration file, and nothing outside fsys is reachable.
func (r *fsRequirer) JumpToAlias(path string) NavResult { return NavNotFound }

func (r *fsRequirer) ToParent() NavResult {
	if r.anonymous {
		// An anonymous chunk lives in the root, so its parent is the root.
		r.anonymous = false
		r.modulePath = ""
		return r.update()
	}
	if r.modulePath == "" {
		return NavNotFound // nothing above the root
	}
	dir := path.Dir(r.modulePath)
	if dir == "." {
		dir = ""
	}
	r.modulePath = dir
	status := r.update()
	if status == NavAmbiguous {
		return NavSuccess // going up is never ambiguous
	}
	return status
}

func (r *fsRequirer) ToChild(name string) NavResult {
	if r.anonymous || name == ".config" || !validName(name) {
		return NavNotFound
	}
	r.modulePath = path.Join(r.modulePath, name)
	return r.update()
}

func (r *fsRequirer) ModulePresent() bool {
	return !r.anonymous && r.isFile(r.realPath)
}

func (r *fsRequirer) Module() (ModuleInfo, error) {
	if !r.ModulePresent() {
		return ModuleInfo{}, errors.New("no module here")
	}
	return ModuleInfo{
		Chunkname: "@" + r.realPath,
		Loadname:  r.realPath,
		CacheKey:  r.realPath,
	}, nil
}

// configPath is where the configuration file called name lives for the
// current location. A module foo.luau keeps its configuration in foo/, and
// foo/init.luau keeps it beside itself.
func (r *fsRequirer) configPath(name string) string {
	dir := r.realPath
	if base := path.Base(dir); base == "init.luau" || base == "init.lua" {
		dir = path.Dir(dir)
	} else {
		for _, suffix := range fileSuffixes {
			if strings.HasSuffix(dir, suffix) {
				dir = strings.TrimSuffix(dir, suffix)
				break
			}
		}
	}
	return path.Join(dir, name)
}

func (r *fsRequirer) ConfigStatus() ConfigStatus {
	json := r.isFile(r.configPath(jsonConfigName))
	luau := r.isFile(r.configPath(luauConfigName))
	switch {
	case json && luau:
		return ConfigAmbiguous
	case luau:
		return ConfigLuau
	case json:
		return ConfigJSON
	}
	return ConfigAbsent
}

func (r *fsRequirer) Config() ([]byte, error) {
	switch r.ConfigStatus() {
	case ConfigJSON:
		return fs.ReadFile(r.fsys, r.configPath(jsonConfigName))
	case ConfigLuau:
		return fs.ReadFile(r.fsys, r.configPath(luauConfigName))
	}
	return nil, errors.New("no configuration file here")
}

func (r *fsRequirer) Source(loadname string) ([]byte, error) {
	if !fs.ValidPath(loadname) {
		return nil, fmt.Errorf("could not read module %q: not a valid path", loadname)
	}
	data, err := fs.ReadFile(r.fsys, loadname)
	if err != nil {
		return nil, fmt.Errorf("could not read module %q: %w", loadname, err)
	}
	return data, nil
}
