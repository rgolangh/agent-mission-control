// Package install puts agent-mission-control and its integrations in place for one
// user: the binary, a systemd user service, the OpenCode bridge plugin, the
// Claude Code hooks plugin, and the desktop UI it detects (omarchy-shell
// widget, GNOME Shell extension). Everything comes from the files embedded in
// the binary, so the integrations always match the installed version.
//
// Rules: install only adds files and never edits an existing configuration
// file. A file is overwritten only if agent-mission-control wrote it and nobody has
// changed it since (tracked in a manifest with SHA-256 sums); a symlink is
// left alone (a development setup linking into a checkout). Commands that
// would change another tool's settings, such as enabling a shell extension,
// are printed, not run, unless Options.Enable is set.
//
// Packaged installs (an Arch or Fedora package that puts the binary in
// /usr/bin, the service in /usr/lib/systemd/user and the GNOME extension in
// /usr/share/gnome-shell/extensions) are detected: when the running binary is
// the packaged one, install uses the packaged service and extension instead
// of per-user copies (which would shadow them and go stale on the next
// package upgrade), and removes per-user copies it wrote earlier.
package install

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Env is the world the installer acts on; tests substitute their own.
type Env struct {
	Home   string                                  // $HOME
	Exe    string                                  // the running binary
	Root   string                                  // prefix for system paths ("" = /); tests set it
	Assets fs.FS                                   // agentmissioncontrol.Assets
	Look   func(cmd string) bool                   // is cmd on PATH?
	Run    func(name string, args ...string) error // run a command (systemctl, ...)
	Out    io.Writer
}

// Options select what to do.
type Options struct {
	Only   map[string]bool // component names; empty = everything detected
	DryRun bool            // print the plan, change nothing
	Force  bool            // overwrite files that are not ours or were modified
	Enable bool            // also run the "enable" commands (shell extension, bar placement)
}

// GNOMEUUID is the GNOME Shell extension's UUID (its directory name).
const GNOMEUUID = "agent-mission-control@rgolangh.github.io"

// Where distribution packages put things (packaging/ in the repository).
const (
	SystemBin   = "/usr/bin/amc"
	SystemUnit  = "/usr/lib/systemd/user/agent-mission-control.service"
	SystemGNOME = "/usr/share/gnome-shell/extensions/" + GNOMEUUID
)

// File is one file to write.
type File struct {
	Path string // absolute
	Data []byte
	Mode fs.FileMode
}

// Component is one installable piece.
type Component struct {
	Name     string
	Detected bool   // the tool it integrates with is present
	Why      string // what was (not) found
	Files    []File
	Post     [][]string // commands run after writing files (best effort)
	Enable   [][]string // commands that change another tool's settings: printed unless Options.Enable
	Notes    []string   // shown after install
	// Supersede lists per-user files an earlier install wrote that a
	// packaged file now replaces: removed if ours and unchanged.
	Supersede []string
	// Skip, if set, is why this component is left alone even if selected
	// (another tool manages it).
	Skip string
}

func (e Env) binDir() string  { return filepath.Join(e.Home, ".local", "bin") }
func (e Env) BinPath() string { return filepath.Join(e.binDir(), "amc") }
func (e Env) unitPath() string {
	return filepath.Join(e.Home, ".config", "systemd", "user", "agent-mission-control.service")
}
func (e Env) omarchyDir() string {
	return filepath.Join(e.Home, ".config", "omarchy", "plugins", "agent-mission-control")
}

// OmarchyGitManaged reports whether the Omarchy plugin directory is a git
// clone, i.e. was put there by `omarchy plugin add <repo>` (which names the
// directory after the manifest id, the same one install uses). Omarchy then
// owns it and updates it with `omarchy plugin update`.
func (e Env) OmarchyGitManaged() bool {
	return exists(filepath.Join(e.omarchyDir(), ".git"))
}

func (e Env) ManifestPath() string {
	return filepath.Join(e.Home, ".local", "share", "agent-mission-control", "install.json")
}

func (e Env) sys(p string) string { return filepath.Join(e.Root, p) }

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// Packaged reports whether the running binary is the one a distribution
// package installed, with the package's service next to it.
func (e Env) Packaged() bool {
	return e.Exe != "" && samePath(e.Exe, e.sys(SystemBin)) && exists(e.sys(SystemUnit))
}

// Unit is the systemd user service, pointing at the installed binary.
func (e Env) Unit() []byte {
	return []byte("# Written by amc install; reinstalling replaces it.\n" + UnitBody(e.BinPath()))
}

// UnitBody is the service without its header comment; the packaged unit
// (packaging/systemd) is UnitBody(SystemBin), which a test checks.
func UnitBody(bin string) string {
	return `[Unit]
Description=agent-mission-control: agent workflow state daemon
Documentation=https://github.com/rgolangh/agent-mission-control

[Service]
ExecStart=` + bin + ` serve -discover
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
`
}

// Plan lists every component, detected or not.
func Plan(e Env) ([]Component, error) {
	var comps []Component

	packaged := e.Packaged()
	core := Component{Name: "daemon", Detected: true, Why: "always"}
	if packaged {
		// The package owns the binary and the unit; a per-user unit of the
		// same name would override the packaged one.
		core.Why = "packaged: " + SystemBin + ", " + SystemUnit
		core.Supersede = []string{e.BinPath(), e.unitPath()}
	} else {
		if e.Exe != "" && !samePath(e.Exe, e.BinPath()) {
			data, err := os.ReadFile(e.Exe)
			if err != nil {
				return nil, err
			}
			core.Files = append(core.Files, File{e.BinPath(), data, 0o755})
		}
		core.Files = append(core.Files, File{e.unitPath(), e.Unit(), 0o644})
	}
	core.Post = [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", "agent-mission-control.service"},
		{"systemctl", "--user", "restart", "agent-mission-control.service"},
	}
	comps = append(comps, core)

	oc := Component{Name: "opencode"}
	oc.Detected, oc.Why = detect(e, "opencode", filepath.Join(e.Home, ".config", "opencode"))
	if f, err := assetFiles(e.Assets, "integrations/opencode", filepath.Join(e.Home, ".config", "opencode", "plugins"), "agent-mission-control-bridge.js"); err == nil {
		oc.Files = f
	} else {
		return nil, err
	}
	oc.Notes = []string{"New `opencode` sessions are discovered automatically; restart open ones."}
	comps = append(comps, oc)

	cc := Component{Name: "claude-code"}
	cc.Detected, cc.Why = detect(e, "claude", filepath.Join(e.Home, ".claude"))
	if f, err := assetFiles(e.Assets, "integrations/claude-code", filepath.Join(e.Home, ".claude", "skills", "agent-mission-control")); err == nil {
		cc.Files = f
	} else {
		return nil, err
	}
	cc.Notes = []string{"Loads in new Claude Code sessions as agent-mission-control@skills-dir (run /reload-plugins in open ones)."}
	comps = append(comps, cc)

	om := Component{Name: "omarchy"}
	om.Detected, om.Why = detect(e, "omarchy-shell", "")
	if f, err := assetFiles(e.Assets, "ui/omarchy", e.omarchyDir()); err == nil {
		om.Files = f
	} else {
		return nil, err
	}
	om.Post = [][]string{{"omarchy-shell", "-q", "shell", "rescanPlugins"}}
	om.Enable = [][]string{{"omarchy", "plugin", "enable", "agent-mission-control"}}
	if e.OmarchyGitManaged() {
		om.Skip = "installed with `omarchy plugin add` (git clone in " + tilde(e, e.omarchyDir()) + "); update it with `omarchy plugin update`"
	}
	comps = append(comps, om)

	gn := Component{Name: "gnome"}
	gn.Detected, gn.Why = detect(e, "gnome-shell", "")
	f, err := assetFiles(e.Assets, "ui/gnome", filepath.Join(e.Home, ".local", "share", "gnome-shell", "extensions", GNOMEUUID))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if packaged && exists(e.sys(SystemGNOME)) {
		// A user extension of the same UUID would shadow the packaged one.
		gn.Why += "; extension packaged in " + SystemGNOME
		for _, x := range f {
			gn.Supersede = append(gn.Supersede, x.Path)
		}
	} else {
		gn.Files = f
	}
	gn.Enable = [][]string{{"gnome-extensions", "enable", GNOMEUUID}}
	gn.Notes = []string{"On Wayland, GNOME Shell loads new extensions after you log out and back in."}
	comps = append(comps, gn)

	return comps, nil
}

func detect(e Env, cmd, dir string) (bool, string) {
	if e.Look != nil && e.Look(cmd) {
		return true, cmd + " found on PATH"
	}
	if dir != "" {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return true, dir + " exists"
		}
	}
	return false, cmd + " not found"
}

// assetFiles maps every embedded file under srcDir to dstDir. If names are
// given, only those files are taken.
func assetFiles(assets fs.FS, srcDir, dstDir string, names ...string) ([]File, error) {
	var out []File
	err := fs.WalkDir(assets, srcDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(p, srcDir+"/")
		if len(names) > 0 && !contains(names, rel) {
			return nil
		}
		data, err := fs.ReadFile(assets, p)
		if err != nil {
			return err
		}
		out = append(out, File{filepath.Join(dstDir, filepath.FromSlash(rel)), data, 0o644})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}

// Manifest records what install wrote, so reinstall and uninstall only touch
// files that are ours and unchanged.
type Manifest struct {
	Version string            `json:"version"`
	Files   map[string]string `json:"files"` // path -> sha256 of what we wrote
	// PackagedService is set when install enabled the packaged service
	// (SystemUnit) rather than writing its own, so uninstall stops it.
	PackagedService bool `json:"packaged_service,omitempty"`
}

func (e Env) LoadManifest() Manifest {
	m := Manifest{Files: map[string]string{}}
	if b, err := os.ReadFile(e.ManifestPath()); err == nil {
		_ = json.Unmarshal(b, &m)
		if m.Files == nil {
			m.Files = map[string]string{}
		}
	}
	return m
}

func (e Env) saveManifest(m Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(e.ManifestPath(), append(b, '\n'), 0o644)
}

// FileState is what is on disk at a planned path, relative to the manifest.
type FileState int

const (
	Missing  FileState = iota // nothing there
	UpToDate                  // identical to what we would write
	Ours                      // we wrote it, unchanged since; differs from the new version
	Modified                  // we wrote it, someone changed it
	Foreign                   // not written by us
	Symlink                   // a symlink (dev setup): never touched
)

func (s FileState) String() string {
	return [...]string{"new", "up to date", "update", "modified locally", "not ours", "symlink (left alone)"}[s]
}

// State classifies path against the manifest and the new content. A path
// that is a symlink, or lies under a symlinked directory below the home
// directory (a dev setup linking a plugin directory into a checkout), is
// Symlink: writing there would modify the checkout.
func State(home string, m Manifest, path string, data []byte) FileState {
	if underSymlink(home, path) {
		return Symlink
	}
	st, err := os.Lstat(path)
	if err != nil {
		return Missing
	}
	if st.Mode()&fs.ModeSymlink != 0 {
		return Symlink
	}
	cur, err := os.ReadFile(path)
	if err != nil {
		return Foreign
	}
	if bytes.Equal(cur, data) {
		return UpToDate
	}
	want, ok := m.Files[path]
	switch {
	case !ok:
		return Foreign
	case want == sum(cur):
		return Ours
	default:
		return Modified
	}
}

// underSymlink reports whether path, or a directory between it and the
// home directory, is a symlink. The home directory's direct children
// (~/.config, ~/.claude, ~/.local) are exempt: dotfile managers often link
// those, and writing through them is what the user intends.
func underSymlink(home, path string) bool {
	rel, err := filepath.Rel(home, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return isSymlink(path)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for n := len(parts); n >= 2; n-- {
		if isSymlink(filepath.Join(append([]string{home}, parts[:n]...)...)) {
			return true
		}
	}
	return false
}

func isSymlink(p string) bool {
	st, err := os.Lstat(p)
	return err == nil && st.Mode()&fs.ModeSymlink != 0
}

// Install applies the plan for the selected components.
func Install(e Env, version string, opt Options) error {
	comps, err := Plan(e)
	if err != nil {
		return err
	}
	m := e.LoadManifest()
	var blocked []string
	var chosen []Component
	for _, c := range comps {
		if !selected(c, opt) {
			why := c.Why
			if len(opt.Only) > 0 {
				why = "not selected"
			}
			fmt.Fprintf(e.Out, "- %-12s skipped (%s)\n", c.Name, why)
			continue
		}
		if c.Skip != "" {
			fmt.Fprintf(e.Out, "- %-12s skipped (%s)\n", c.Name, c.Skip)
			continue
		}
		if len(c.Files) == 0 && len(c.Supersede) == 0 {
			fmt.Fprintf(e.Out, "- %-12s nothing to install in this build\n", c.Name)
			continue
		}
		chosen = append(chosen, c)
		fmt.Fprintf(e.Out, "+ %-12s (%s)\n", c.Name, c.Why)
		for _, p := range c.Supersede {
			switch superseded(e, m, p) {
			case Ours, UpToDate:
				fmt.Fprintf(e.Out, "    %-22s %s\n", "remove (packaged)", tilde(e, p))
			case Missing, Symlink:
			default:
				fmt.Fprintf(e.Out, "    %-22s %s\n", "WARNING: shadows package", tilde(e, p))
			}
		}
		for _, f := range c.Files {
			s := State(e.Home, m, f.Path, f.Data)
			fmt.Fprintf(e.Out, "    %-22s %s\n", s, tilde(e, f.Path))
			if (s == Foreign || s == Modified) && !opt.Force {
				blocked = append(blocked, f.Path)
			}
		}
	}
	if len(blocked) > 0 {
		return fmt.Errorf("refusing to overwrite %d file(s) not written by agent-mission-control or changed since (use --force): %s",
			len(blocked), strings.Join(blocked, ", "))
	}
	if opt.DryRun {
		fmt.Fprintln(e.Out, "dry run: nothing changed")
		return nil
	}

	dirs := map[string]bool{}
	for _, c := range chosen {
		for _, p := range c.Supersede {
			if s := superseded(e, m, p); s == Ours || s == UpToDate {
				if err := os.Remove(p); err != nil {
					return err
				}
				delete(m.Files, p)
				dirs[filepath.Dir(p)] = true
			}
		}
		for _, f := range c.Files {
			switch State(e.Home, m, f.Path, f.Data) {
			case Symlink:
				continue
			case UpToDate:
				m.Files[f.Path] = sum(f.Data)
				continue
			}
			if err := writeAtomic(f.Path, f.Data, f.Mode); err != nil {
				return err
			}
			m.Files[f.Path] = sum(f.Data)
		}
	}
	removeEmptyDirs(e, dirs)
	for _, c := range chosen {
		if c.Name == "daemon" && len(c.Files) == 0 {
			m.PackagedService = true
		}
	}
	m.Version = version
	if err := e.saveManifest(m); err != nil {
		return err
	}

	for _, c := range chosen {
		for _, cmd := range c.Post {
			if e.Look != nil && !e.Look(cmd[0]) {
				continue
			}
			if err := e.Run(cmd[0], cmd[1:]...); err != nil {
				fmt.Fprintf(e.Out, "  warning: %s: %v\n", strings.Join(cmd, " "), err)
			}
		}
		for _, cmd := range c.Enable {
			if opt.Enable {
				if err := e.Run(cmd[0], cmd[1:]...); err != nil {
					fmt.Fprintf(e.Out, "  warning: %s: %v\n", strings.Join(cmd, " "), err)
				}
			} else {
				fmt.Fprintf(e.Out, "  to enable %s: %s\n", c.Name, strings.Join(cmd, " "))
			}
		}
		for _, n := range c.Notes {
			fmt.Fprintf(e.Out, "  note: %s\n", n)
		}
	}
	return nil
}

// superseded classifies a per-user file a package now replaces: UpToDate if
// it is exactly what install wrote (so it may go), Foreign or Modified if
// someone else's, Missing or Symlink if there is nothing to do.
func superseded(e Env, m Manifest, p string) FileState {
	if underSymlink(e.Home, p) {
		return Symlink
	}
	cur, err := os.ReadFile(p)
	if err != nil {
		return Missing
	}
	want, ok := m.Files[p]
	switch {
	case !ok:
		return Foreign
	case want == sum(cur):
		return UpToDate
	default:
		return Modified
	}
}

// Uninstall removes what install wrote and is unchanged, and stops the
// service. Modified files are kept unless Force.
func Uninstall(e Env, opt Options) error {
	m := e.LoadManifest()
	if len(m.Files) == 0 && !m.PackagedService {
		fmt.Fprintln(e.Out, "nothing installed by amc install")
		return nil
	}
	// Only touch the service if install put it there (or enabled the
	// packaged one): an uninstall of, say, just the GNOME extension must not
	// stop a daemon set up some other way.
	_, ownsUnit := m.Files[e.unitPath()]
	ownsUnit = ownsUnit || m.PackagedService
	if ownsUnit && !opt.DryRun && e.Look != nil && e.Look("systemctl") {
		_ = e.Run("systemctl", "--user", "disable", "--now", "agent-mission-control.service")
	}
	paths := make([]string, 0, len(m.Files))
	for p := range m.Files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	dirs := map[string]bool{}
	for _, p := range paths {
		// Install never writes through a symlink, and uninstall must not
		// delete through one: the path may now lead into a checkout.
		if underSymlink(e.Home, p) {
			fmt.Fprintf(e.Out, "  kept       %s (symlink: left alone)\n", tilde(e, p))
			delete(m.Files, p)
			continue
		}
		// Replaced by `omarchy plugin add` since: its clone's files may
		// match what install wrote, but they are omarchy's now.
		if e.OmarchyGitManaged() && strings.HasPrefix(p, e.omarchyDir()+string(filepath.Separator)) {
			fmt.Fprintf(e.Out, "  kept       %s (managed by omarchy plugin)\n", tilde(e, p))
			delete(m.Files, p)
			continue
		}
		cur, err := os.ReadFile(p)
		switch {
		case err != nil:
			fmt.Fprintf(e.Out, "  gone       %s\n", tilde(e, p))
		case sum(cur) != m.Files[p] && !opt.Force:
			fmt.Fprintf(e.Out, "  kept       %s (modified since install)\n", tilde(e, p))
			continue
		default:
			fmt.Fprintf(e.Out, "  remove     %s\n", tilde(e, p))
			if !opt.DryRun {
				if err := os.Remove(p); err != nil {
					return err
				}
			}
		}
		delete(m.Files, p)
		dirs[filepath.Dir(p)] = true
	}
	if opt.DryRun {
		fmt.Fprintln(e.Out, "dry run: nothing changed")
		return nil
	}
	removeEmptyDirs(e, dirs)
	if len(m.Files) == 0 {
		_ = os.Remove(e.ManifestPath())
		_ = os.Remove(filepath.Dir(e.ManifestPath()))
	} else if err := e.saveManifest(m); err != nil {
		return err
	}
	if ownsUnit && e.Look != nil && e.Look("systemctl") {
		_ = e.Run("systemctl", "--user", "daemon-reload")
	}
	return nil
}

// removeEmptyDirs removes directories install created that are now empty,
// walking up but never past well-known roots (it stops at the first
// non-empty directory, so ~/.config and friends always survive).
func removeEmptyDirs(e Env, dirs map[string]bool) {
	stop := map[string]bool{e.Home: true}
	for _, d := range []string{".config", ".local", ".local/bin", ".local/share", ".claude", ".claude/skills",
		".config/opencode", ".config/opencode/plugins", ".config/omarchy", ".config/omarchy/plugins",
		".config/systemd", ".config/systemd/user", ".local/share/gnome-shell", ".local/share/gnome-shell/extensions"} {
		stop[filepath.Join(e.Home, d)] = true
	}
	list := make([]string, 0, len(dirs))
	for d := range dirs {
		list = append(list, d)
	}
	sort.Slice(list, func(i, j int) bool { return len(list[i]) > len(list[j]) }) // deepest first
	for _, d := range list {
		for ; !stop[d] && strings.HasPrefix(d, e.Home+string(filepath.Separator)); d = filepath.Dir(d) {
			if os.Remove(d) != nil { // fails unless empty
				break
			}
		}
	}
}

func selected(c Component, opt Options) bool {
	if len(opt.Only) > 0 {
		return opt.Only[c.Name]
	}
	return c.Detected
}

func writeAtomic(p string, data []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".mc-tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p)
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func samePath(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func tilde(e Env, p string) string {
	if rel, ok := strings.CutPrefix(p, e.Home+"/"); ok {
		return "~/" + rel
	}
	return p
}
