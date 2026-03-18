BINARY  := tltv
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION)"

.PHONY: build install test clean release

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
	@for pair in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64 freebsd/amd64; do \
		GOOS=$${pair%/*}; \
		GOARCH=$${pair#*/}; \
		BIN=$(BINARY); \
		if [ "$$GOOS" = "windows" ]; then BIN=$(BINARY).exe; fi; \
		echo "Building $$GOOS/$$GOARCH..."; \
		CGO_ENABLED=0 GOOS=$$GOOS GOARCH=$$GOARCH go build $(LDFLAGS) -o dist/$$BIN .; \
		ARCHIVE=$(BINARY)-cli_$(VERSION)_$$GOOS-$$GOARCH; \
		tar -czf dist/$$ARCHIVE.tar.gz -C dist $$BIN; \
		rm dist/$$BIN; \
	done
	@cd dist && sha256sum * > checksums.txt
	@echo "Release archives in dist/"
	@ls -lh dist/
