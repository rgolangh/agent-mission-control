# Fedora / COPR package for agent-mission-control. Version is bumped with
# packaging/version.sh; see packaging/README.md.

# Run the Go tests in %%check (rpmbuild --without check to skip).
%bcond check 1

# %%gobuild builds in GOPATH mode unless told otherwise; this is a module.
%global gomodulesmode GO111MODULE=on

%global uuid agent-mission-control@rgolangh.github.io

Name:           agent-mission-control
Version:        0.1.2
Release:        1%{?dist}
Summary:        One view of your coding-agent sessions: working, waiting on you, done or stuck

License:        Apache-2.0
URL:            https://github.com/rgolangh/agent-mission-control
# Release asset made by packaging/dist.sh (git archive of the tag).
Source0:        %{url}/releases/download/v%{version}/%{name}-%{version}.tar.gz

# Standard library only: no bundled Go modules.
ExclusiveArch:  %{golang_arches}
BuildRequires:  golang >= 1.27
BuildRequires:  go-rpm-macros
BuildRequires:  gcc
BuildRequires:  systemd-rpm-macros

# The desktop UI for GNOME, if GNOME is installed.
Recommends:     (gnome-shell-extension-agent-mission-control if gnome-shell)

%description
agent-mission-control (amc) is a daemon, CLI and MCP server that shows the
state of every coding-agent session (OpenCode, Claude Code) in one place:
who is working, who is waiting on you, who finished and who is stuck, and
lets you or an orchestrating agent answer them.

After installing, run `amc install --enable` once as each user: it enables
the packaged user service and installs the agent integrations it detects.

%package -n gnome-shell-extension-agent-mission-control
Summary:        GNOME Shell indicator for agent-mission-control
BuildArch:      noarch
Requires:       %{name} = %{version}-%{release}
Requires:       gnome-shell >= 45

%description -n gnome-shell-extension-agent-mission-control
A top-bar indicator and menu for agent-mission-control: how many coding
agents need you, the list of workflows, and answering their questions.
Enable it per user with `amc install --enable` or
`gnome-extensions enable %{uuid}`.

%prep
%autosetup -p1

%build
# go.mod's toolchain line would otherwise make go download a newer toolchain.
export GOTOOLCHAIN=local
export LDFLAGS="-X main.version=v%{version}"
%gobuild -o build/amc ./cmd/amc

%install
install -Dpm 0755 build/amc %{buildroot}%{_bindir}/amc
install -Dpm 0644 packaging/systemd/agent-mission-control.service \
    %{buildroot}%{_userunitdir}/agent-mission-control.service
install -d %{buildroot}%{_datadir}/gnome-shell/extensions/%{uuid}
install -pm 0644 ui/gnome/metadata.json ui/gnome/extension.js ui/gnome/client.js \
    ui/gnome/model.js ui/gnome/stylesheet.css \
    %{buildroot}%{_datadir}/gnome-shell/extensions/%{uuid}/
# The per-user integrations, linked into each home by amc install.
packaging/install-share.sh %{buildroot}%{_datadir}/%{name}

%check
%if %{with check}
export GOTOOLCHAIN=local GO111MODULE=on
go test ./...
%endif

%post
%systemd_user_post agent-mission-control.service

%preun
%systemd_user_preun agent-mission-control.service

%postun
%systemd_user_postun_with_restart agent-mission-control.service

%files
%license LICENSE
%doc README.md SECURITY.md
%{_bindir}/amc
%{_userunitdir}/agent-mission-control.service
%{_datadir}/%{name}/

%files -n gnome-shell-extension-agent-mission-control
%license LICENSE
%{_datadir}/gnome-shell/extensions/%{uuid}/

%changelog
* Wed Oct 21 2026 Roy Golan <royg21@gmail.com> - 0.1.2-1
- Release v0.1.2

* Tue Oct 20 2026 Roy Golan <royg21@gmail.com> - 0.1.1-1
- First RPM packaging
