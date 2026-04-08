BINARY  := tltv
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION)"

.PHONY: build install test clean release deb

build:
	CGO_ENABLED=0 go build $(LDFLAGS) -o $(BINARY) .

install:
	CGO_ENABLED=0 go install $(LDFLAGS) .

test:
	go test -v ./...

clean:
	rm -f $(BINARY)
	rm -rf dist/

# Cross-compile for all major platforms (static binaries, no cgo).
# Produces tar.gz archives plus checksums.
release: clean
	@mkdir -p dist
	@	for pair in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64 freebsd/amd64; do \
		GOOS=$${pair%/*}; \
		GOARCH=$${pair#*/}; \
		BIN=$(BINARY); \
		if [ "$$GOOS" = "windows" ]; then BIN=$(BINARY).exe; fi; \
		echo "Building $$GOOS/$$GOARCH..."; \
		CGO_ENABLED=0 GOOS=$$GOOS GOARCH=$$GOARCH go build $(LDFLAGS) -o dist/$$BIN .; \
		ARCHIVE=$(BINARY)-cli_$(VERSION)_$$GOOS-$$GOARCH; \
		if [ "$$GOOS" = "windows" ]; then \
			(cd dist && zip $$ARCHIVE.zip $$BIN); \
		else \
			tar -czf dist/$$ARCHIVE.tar.gz -C dist $$BIN; \
		fi; \
		rm dist/$$BIN; \
	done
	@cd dist && sha256sum * > checksums.txt
	@echo "Release archives in dist/"
	@ls -lh dist/

# Build .deb package for a given GOARCH (default: amd64).
# Usage: make deb  OR  make deb GOARCH=arm64
DEB_ARCH ?= amd64
deb:
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=$(DEB_ARCH) go build $(LDFLAGS) -o dist/tltv-deb .
	@mkdir -p dist/pkg/DEBIAN dist/pkg/usr/bin
	@cp dist/tltv-deb dist/pkg/usr/bin/tltv
	@DEB_ARCH_NAME=$(DEB_ARCH); \
	if [ "$$DEB_ARCH_NAME" = "amd64" ]; then DEB_ARCH_NAME=amd64; fi; \
	if [ "$$DEB_ARCH_NAME" = "arm64" ]; then DEB_ARCH_NAME=arm64; fi; \
	printf 'Package: tltv\nVersion: %s\nArchitecture: %s\nMaintainer: Philo Farnsworth <farnsworth27@protonmail.com>\nDescription: Command-line tool for the TLTV Federation Protocol\n Single static binary, zero dependencies.\n' \
	"$(VERSION)" "$$DEB_ARCH_NAME" > dist/pkg/DEBIAN/control
	dpkg-deb --build dist/pkg "dist/tltv-cli_$(VERSION)_$(DEB_ARCH).deb"
	@rm -rf dist/pkg dist/tltv-deb
	@echo "Built dist/tltv-cli_$(VERSION)_$(DEB_ARCH).deb"
