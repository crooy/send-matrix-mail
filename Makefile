VERSION := $(shell cat VERSION 2>/dev/null || echo "dev")
COMMIT  := $(shell jj log -r '@' --no-graph 2>/dev/null | awk '{print $$1; exit}')
GO      ?= go

# Embed version and optionally commit hash. Non-release builds get "-dev-<hash>".
ifeq ($(VERSION),dev)
	LDFLAGS := -ldflags="-X main.version=$(VERSION)"
else ifneq ($(COMMIT),)
	LDFLAGS := -ldflags="-X main.version=$(VERSION)-dev-$(COMMIT)"
else
	LDFLAGS := -ldflags="-X main.version=$(VERSION)"
endif

# Release builds: VERSION from file must be a number, no commit suffix
release: LDFLAGS := -ldflags="-X main.version=$(VERSION)"
release: VERSION_CHECK := $(shell echo $(VERSION) | grep -q '^[0-9]' && echo ok)
ifeq ($(VERSION_CHECK),ok)
release: clean build dist deb
endif

BINNAME  ?= send-matrix-mail
BINDIR   ?= /usr/local/bin

.PHONY: all build test vet clean install dist deb help release

all: build

# ── Build ──────────────────────────────────────────────────────────

build:
	$(GO) build $(LDFLAGS) -o $(BINNAME) .

test:
	$(GO) test -count=1 ./internal/...

vet:
	$(GO) vet ./...

clean:
	rm -f $(BINNAME)
	rm -rf dist/

install: build
	install -d $(DESTDIR)$(BINDIR)
	install -m 755 $(BINNAME) $(DESTDIR)$(BINDIR)/$(BINNAME)

# ── Cross-compile tarballs ─────────────────────────────────────────

ARCHS = amd64 arm64

dist: $(addprefix dist-,$(ARCHS))
dist-amd64: GOARCH=amd64
dist-arm64: GOARCH=arm64
dist-%:
	@mkdir -p dist/$(BINNAME)-$(VERSION)-linux-$*
	GOOS=linux GOARCH=$* $(GO) build $(LDFLAGS) \
		-o dist/$(BINNAME)-$(VERSION)-linux-$*/$(BINNAME) .
	cp README.md send-matrix-mail.toml.example dist/$(BINNAME)-$(VERSION)-linux-$*/
	cd dist && tar czf $(BINNAME)-$(VERSION)-linux-$*.tar.gz $(BINNAME)-$(VERSION)-linux-$*
	rm -rf dist/$(BINNAME)-$(VERSION)-linux-$*
	@echo "dist/$(BINNAME)-$(VERSION)-linux-$*.tar.gz"

# ── Debian package ─────────────────────────────────────────────────

DEB_AMD64 := dist/$(BINNAME)_$(VERSION)_amd64.deb
DEB_ARM64 := dist/$(BINNAME)_$(VERSION)_arm64.deb

deb: $(DEB_AMD64) $(DEB_ARM64)
deb-amd64: $(DEB_AMD64)
deb-arm64: $(DEB_ARM64)

$(DEB_AMD64): GOARCH=amd64
$(DEB_ARM64): GOARCH=arm64

dist/$(BINNAME)_$(VERSION)_%.deb:
	$(eval ARCH := $*)
	$(eval DEBDIR := dist/$(BINNAME)_$(VERSION)_$(ARCH))
	@mkdir -p $(DEBDIR)/DEBIAN
	@mkdir -p $(DEBDIR)/usr/bin
	@mkdir -p $(DEBDIR)/usr/share/doc/$(BINNAME)
	@mkdir -p $(DEBDIR)/usr/share/man/man1
	@mkdir -p $(DEBDIR)/etc/$(BINNAME)
	@mkdir -p $(DEBDIR)/var/lib/$(BINNAME)/spool

	GOOS=linux GOARCH=$(ARCH) $(GO) build $(LDFLAGS) \
		-o $(DEBDIR)/usr/bin/$(BINNAME) .
	# Config file (the one users edit) goes to /etc/
	install -m 644 send-matrix-mail.toml.example $(DEBDIR)/etc/$(BINNAME)/send-matrix-mail.toml
	# Example config and README go to /usr/share/doc/
	install -m 644 send-matrix-mail.toml.example $(DEBDIR)/usr/share/doc/$(BINNAME)/send-matrix-mail.toml.example
	install -m 644 README.md $(DEBDIR)/usr/share/doc/$(BINNAME)/
	gzip -9fn $(DEBDIR)/usr/share/doc/$(BINNAME)/README.md
	install -m 644 packaging/$(BINNAME).1 $(DEBDIR)/usr/share/man/man1/
	gzip -9fn $(DEBDIR)/usr/share/man/man1/$(BINNAME).1
	install -m 755 packaging/debian/postinst $(DEBDIR)/DEBIAN/postinst
	install -m 755 packaging/debian/prerm   $(DEBDIR)/DEBIAN/prerm
	install -m 644 packaging/debian/conffiles $(DEBDIR)/DEBIAN/conffiles
	sed 's/^Architecture:.*/Architecture: $(ARCH)/' packaging/debian/control \
	  | sed 's/^Version:.*/Version: $(VERSION)/' \
	  > $(DEBDIR)/DEBIAN/control \
	  && echo '' >> $(DEBDIR)/DEBIAN/control
	dpkg-deb --build --root-owner-group -Zgzip $(DEBDIR)
	@echo "dist/$(BINNAME)_$(VERSION)_$(ARCH).deb"

# ── Help ───────────────────────────────────────────────────────────

help:
	@echo "Targets:"
	@echo "  make build        — build with dev version string"
	@echo "  make test         — run tests"
	@echo "  make vet          — run go vet"
	@echo "  make install      — install to \$$DESTDIR$(BINDIR)"
	@echo "  make dist         — build tarballs for $(ARCHS)"
	@echo "  make deb          — build .deb for $(ARCHS)"
	@echo "  make release      — build+dist+deb (VERSION must be a release number)"
	@echo "  make clean        — remove build artifacts"
	@echo ""
	@echo "Variables:"
	@echo "  VERSION=$(VERSION)   GO=$(GO)"