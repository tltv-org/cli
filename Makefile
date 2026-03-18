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

# Cross-compile for all major platforms (static binaries, no cgo)
release: clean
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY)-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build $(LDFLAGS) -o dist/$(BINARY)-linux-arm64 .
	CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY)-darwin-amd64 .
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build $(LDFLAGS) -o dist/$(BINARY)-darwin-arm64 .
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY)-windows-amd64.exe .
	@echo "Built binaries in dist/"
	@ls -lh dist/
