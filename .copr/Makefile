# COPR "make srpm" build method (see packaging/README.md): COPR clones the
# repository at the pushed ref and runs `make -f .copr/Makefile srpm
# outdir=... spec=...`. The source tarball is made from that checkout the
# same way packaging/dist.sh makes the release asset.
spec ?= packaging/rpm/agent-mission-control.spec
outdir ?= .

srpm:
	command -v git >/dev/null && command -v rpmbuild >/dev/null || dnf -y install git-core rpm-build
	v=$$(sed -n 's/^Version:[[:space:]]*//p' $(spec)); \
	git -c safe.directory='*' archive --format=tar.gz --prefix=agent-mission-control-$$v/ \
		-o $(outdir)/agent-mission-control-$$v.tar.gz HEAD && \
	rpmbuild -bs --define "_sourcedir $(outdir)" --define "_srcrpmdir $(outdir)" $(spec)

.PHONY: srpm
