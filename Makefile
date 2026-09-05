# Convenience targets. Everything here is also what CI runs, so a green `make check`
# locally means a green pull request.

BINARY  := noisecrypt
PKG     := github.com/bzhzion/noisecrypt
VERSION := $(shell git describe --tags --abbrev=0 --match 'v[0-9]*' 2>/dev/null | sed 's/^v//' || echo dev)
LDFLAGS := -s -w -X $(PKG)/internal/cli.Version=$(VERSION)

.PHONY: all build test vet fmt fmt-check check vuln clean hooks

all: check build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/noisecrypt

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

fmt-check:
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "Not gofmt clean:"; echo "$$unformatted" | sed 's/^/    /'; exit 1; \
	fi

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

check: fmt-check vet test

# core.hooksPath is local configuration that git never transports, so a fresh clone
# has no hooks until this runs. Re-run it after every clone.
hooks:
	git config core.hooksPath .githooks
	@echo "Hooks enabled from .githooks"

clean:
	rm -f $(BINARY) $(BINARY).exe
	rm -rf dist
