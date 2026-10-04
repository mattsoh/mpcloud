PREFIX ?= /usr/local
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test install uninstall snapshot clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o mpcloud ./cmd/mpcloud

test:
	go vet ./...
	go test ./...

# Installs the binary and the `printer` shortcut. Afterwards, optionally:
#   sudo mpcloud install-cups
install: build
	install -d $(DESTDIR)$(PREFIX)/bin
	install -m 0755 mpcloud $(DESTDIR)$(PREFIX)/bin/mpcloud
	ln -sf mpcloud $(DESTDIR)$(PREFIX)/bin/printer
	install -d $(DESTDIR)$(PREFIX)/share/applications
	install -m 0644 packaging/mpcloud-handler.desktop $(DESTDIR)$(PREFIX)/share/applications/
	@if [ -x /usr/lib/cups/backend/mpcloud ]; then install -m 0755 mpcloud /usr/lib/cups/backend/mpcloud; fi

uninstall:
	-$(DESTDIR)$(PREFIX)/bin/mpcloud uninstall-cups
	rm -f $(DESTDIR)$(PREFIX)/bin/mpcloud $(DESTDIR)$(PREFIX)/bin/printer
	rm -f $(DESTDIR)$(PREFIX)/share/applications/mpcloud-handler.desktop

# Local test build of all release artifacts (needs goreleaser).
snapshot:
	goreleaser release --snapshot --clean

clean:
	rm -rf mpcloud dist
