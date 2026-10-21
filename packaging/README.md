# Packaging

| Path | What |
|---|---|
| `version.sh` | Sets/checks the version in the files that carry it outside the binary: `ui/omarchy/manifest.json`, `ui/gnome/metadata.json`, `rpm/agent-mission-control.spec` |
| `dist.sh` | Builds the release assets into `dist/` (also what CI runs) |
| `systemd/agent-mission-control.service` | The packaged user unit (`/usr/bin/amc serve -discover`). A test keeps it identical to the one `amc install` writes |
| `arch/agent-mission-control-bin/` | AUR package repackaging the release tarballs (x86_64, aarch64) |
| `arch/agent-mission-control/` | AUR package building from the source tarball |
| `arch/update.sh` | Points both PKGBUILDs at a release (pkgver, sums, `.SRCINFO`) |
| `arch/build.sh` | Builds the `-bin` packages from a local `dist/` (CI) |
| `rpm/agent-mission-control.spec` | Fedora/COPR spec: `agent-mission-control` + noarch `gnome-shell-extension-agent-mission-control` |
| `../.copr/Makefile` | COPR's "make srpm" hook |

## How packages and `amc install` fit together

A package installs `/usr/bin/amc`, `/usr/lib/systemd/user/agent-mission-control.service`
and the GNOME extension under `/usr/share/gnome-shell/extensions/`. It enables
nothing: user services and per-user integrations are each user's call, so
the post-install message says to run `amc install --enable`.

When the running `amc` *is* `/usr/bin/amc` and the packaged unit exists,
`amc install` is in packaged mode (`install.Env.Packaged`): it writes no
`~/.local/bin/amc` and no `~/.config/systemd/user/…` unit (a per-user unit of
the same name would override the packaged one), just enables the packaged
service; it skips the per-user GNOME extension when the packaged one is
there; and it removes those per-user files if an earlier non-packaged
`amc install` wrote them and they are unchanged (anything else is kept, with
a warning that it shadows the package). A dev build run from elsewhere is
not packaged mode, so it deliberately overrides the package per user.
`amc uninstall` disables the packaged service if install enabled it.

The OpenCode, Claude Code and Omarchy integrations are always per user
(those tools only look in `$HOME`), written from the files embedded in the
binary. After a package upgrade users rerun `amc install` (the post-upgrade
message says so; `amc doctor` flags stale copies).

Omarchy: `omarchy plugin add <repo>` clones into the same directory `amc
install` uses (`~/.config/omarchy/plugins/agent-mission-control`, named after
the manifest id). If that directory is a git clone, `amc install` skips the
omarchy component (updates come from `omarchy plugin update`) and `amc
uninstall` leaves it alone. Recommended for package users: `amc install`,
because the embedded widget always matches the daemon's API.

## Cutting a release

```bash
make release V=0.2.0
```

On a clean, up-to-date `main` it runs `packaging/version.sh set 0.2.0`
(manifest, GNOME metadata, spec and its changelog entry), commits "Release
v0.2.0", tags `v0.2.0` and pushes both. The workflow never bumps anything
itself, it only checks that the files match the tag.

`.github/workflows/release.yml` then:

1. **build**: fails unless `version.sh check` matches the tag; tests; `dist.sh`.
2. **arch**: in `archlinux:base-devel`, `arch/update.sh` + `arch/build.sh`
   (makepkg, x86_64 and aarch64), and packs the updated AUR files.
3. **rpm**: in `fedora:$FEDORA` on an x86_64 and an arm64 runner, `rpmbuild -ba`
   from the source tarball (rpmlint output in the log, not fatal).
4. **release**: one GitHub release with everything and a fresh `SHA256SUMS`.
5. **omarchy-plugin**: mirrors `ui/omarchy` (see below), if configured.

Pull requests touching `packaging/`, `.copr/`, `release.yml` or `go.mod`
(and manual runs) run build, arch and rpm at the version in the files, so
packaging breakage shows up before a tag. Only tags run release and
omarchy-plugin.

Release assets (`X.Y.Z` = tag without `v`):

| Asset | |
|---|---|
| `amc-linux-{amd64,arm64}` | Bare static binaries (README's curl install; unversioned so `latest/download/` works) |
| `amc-X.Y.Z-linux-{amd64,arm64}.tar.gz` | Binary, unit, GNOME extension, LICENSE, docs: what `-bin` repackages |
| `agent-mission-control-X.Y.Z.tar.gz` | Source (`git archive` of the tag): the source PKGBUILD's and the spec's `Source0` |
| `agent-mission-control-bin-X.Y.Z-1-{x86_64,aarch64}.pkg.tar.zst` | Arch packages |
| `agent-mission-control-X.Y.Z-1.fcNN.{x86_64,aarch64}.rpm`, `gnome-shell-extension-agent-mission-control-X.Y.Z-1.fcNN.noarch.rpm`, `.src.rpm` | Fedora packages |
| `aur-X.Y.Z.tar.gz` | Both AUR package directories, updated for this release |
| `agent-mission-control@rgolangh.github.io.shell-extension.zip` | For `gnome-extensions install` / extensions.gnome.org |
| `SHA256SUMS` | Of all of the above |

Why native tools in containers rather than nfpm: the PKGBUILDs and the spec
have to exist anyway (AUR and COPR consume them), so building the release
packages from those same files means every release tests what the AUR and
COPR will build, with no third packaging definition to drift from them. The
cost is two container jobs and the rpm matrix.

Asset names before v0.2.0 differ (v0.1.1 shipped `mission-control-linux-*`),
so the PKGBUILDs only work from the first release made with this workflow.

## AUR

One-time: an AUR account with your SSH key
(<https://aur.archlinux.org/account>), then for each package:

```bash
git clone ssh://aur@aur.archlinux.org/agent-mission-control-bin.git
git clone ssh://aur@aur.archlinux.org/agent-mission-control.git
```

(cloning a name that doesn't exist yet creates it on first push).

Each release, after the workflow finished:

```bash
gh release download v0.2.0 -p 'aur-*.tar.gz' -D /tmp/aur
tar -xzf /tmp/aur/aur-0.2.0.tar.gz -C /tmp/aur
for p in agent-mission-control-bin agent-mission-control; do
  cp /tmp/aur/$p/* /tmp/aur/$p/.SRCINFO ~/aur/$p/
  (cd ~/aur/$p && makepkg -f && namcap PKGBUILD *.pkg.tar.zst; git add -A && git commit -m "Update to 0.2.0" && git push)
done
```

Or, without the asset, update the in-repo PKGBUILDs:
`gh release download v0.2.0 -p SHA256SUMS -D /tmp/r && packaging/arch/update.sh v0.2.0 /tmp/r/SHA256SUMS`
(commit that too). The in-repo `agent-mission-control.install` of the source
package is a symlink: copy with `cp -L`. The source package needs
`go>=2:1.27`; it builds with `GOTOOLCHAIN=local`, so with whatever 1.27.x
Arch has (go.mod's `toolchain go1.27.2` is a preference, not a requirement).

## COPR (Fedora)

One-time:

1. Log in at <https://copr.fedorainfracloud.org> (Fedora account `rgolan`), create
   project `agent-mission-control` with chroots `fedora-45-x86_64`,
   `fedora-45-aarch64`, `fedora-rawhide-*` (needs `golang >= 1.27`: Fedora
   44's Go is too old for `go 1.27` in go.mod).
2. Add a package, source type **SCM**: clone URL
   `https://github.com/rgolangh/agent-mission-control`, spec
   `packaging/rpm/agent-mission-control.spec`, SRPM build method
   **make srpm** (uses `.copr/Makefile`, which archives the checkout).
3. Optional, automatic builds: in the project's Integrations page copy the
   GitHub webhook URL and add it to the repository (Settings → Webhooks,
   "tag push" events; restrict the package's "committish" to tags if you
   don't want builds of every push).

Manual alternative per release: `copr-cli build agent-mission-control
agent-mission-control-0.2.0-1.fc45.src.rpm` with the SRPM from the release
(needs `~/.config/copr` from <https://copr.fedorainfracloud.org/api/>).

Users: `sudo dnf copr enable rgolan/agent-mission-control && sudo dnf install agent-mission-control`.
The GNOME extension subpackage comes in through a weak dependency when
`gnome-shell` is installed.

## Omarchy plugin mirror

`omarchy plugin add <git-url>` wants `manifest.json` at the repository root,
so the release workflow's `omarchy-plugin` job pushes
`git subtree split --prefix=ui/omarchy` to the `main` branch (and the release
tag) of `rgolangh/omarchy-agent-mission-control`. The split is deterministic,
so each push is a fast-forward; it is never forced (commits made directly in
the mirror make it fail rather than be lost). Until the secret exists the job
only prints a notice.

One-time:

1. Create the empty repository `rgolangh/omarchy-agent-mission-control`
   (public, no README).
2. `ssh-keygen -t ed25519 -N '' -C omarchy-plugin-mirror -f /tmp/mirror`
3. In the mirror repo: Settings → Deploy keys → add `/tmp/mirror.pub`,
   **allow write access**.
4. In this repo: Settings → Secrets and variables → Actions → New repository
   secret `OMARCHY_PLUGIN_DEPLOY_KEY` = contents of `/tmp/mirror`. Then delete
   `/tmp/mirror*`.
5. Optional: repository variable `OMARCHY_PLUGIN_REPO` (`owner/name`) to
   mirror somewhere else.

A deploy key is used instead of a personal access token because it can write
to that one repository and nothing else.

## Secrets and variables

| Name | Kind | Used by | Needed for |
|---|---|---|---|
| `GITHUB_TOKEN` | automatic | release | creating the release |
| `OMARCHY_PLUGIN_DEPLOY_KEY` | secret | omarchy-plugin | the plugin mirror (job is a no-op without it) |
| `OMARCHY_PLUGIN_REPO` | variable, optional | omarchy-plugin | mirror target (default `rgolangh/omarchy-agent-mission-control`) |

AUR and COPR publishing is manual (or COPR's webhook): no secrets for them in CI.
