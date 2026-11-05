# 13s-patcher — 13 Sentinels Unburger Patch Builder
#
# Everything is vendored, so these targets work offline.
#
# VERSION is the patcher *application* version (the Go binary). It is not the
# Unburger Mod version: the mod version is read at run time from ../version.txt,
# the single authoritative source, and is shown by the dashboard and used as the
# default release label for output file names.
VERSION ?= 0.1.0
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "")
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
  -X 13s-patcher/internal/build.ToolVersion=$(VERSION) \
  -X 13s-patcher/internal/build.Commit=$(COMMIT) \
  -X 13s-patcher/internal/build.BuildDate=$(DATE)
GOFLAGS := -mod=vendor
CGO_ENABLED ?= 0
export CGO_ENABLED

BIN := bin/13s-patcher

.PHONY: all build test vet hygiene hygiene-changed hygiene-fix release-check isolation demo release fmt clean install

all: build

build:
	mkdir -p bin
	go build $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/13s-patcher

install:
	go install $(GOFLAGS) -trimpath -ldflags "$(LDFLAGS)" ./cmd/13s-patcher

test:
	go test $(GOFLAGS) ./...

vet:
	go vet $(GOFLAGS) ./...

# Full public-boundary check: repository-root scan (contents, names, shipped
# asset metadata, vendor project markers, read-only Git checks), the hygiene
# regression tests, and the isolated release-boundary scan.
hygiene:
	./scripts/check_public_hygiene.sh
	go test $(GOFLAGS) ./internal/hygiene/
	./scripts/check_release_boundary.sh

# Fast after-edit gate: only files Git reports as changed/untracked, or an
# explicit file list. Use this immediately after every public-file edit.
hygiene-changed:
	./scripts/check_public_hygiene.sh --changed

# Safe auto-correction for the changed set (gofmt and machine-text whitespace),
# then rescan until stable. Semantic findings are never auto-rewritten.
hygiene-fix:
	./scripts/check_public_hygiene.sh --changed --fix

release-check:
	./scripts/check_release_boundary.sh

isolation:
	./scripts/check_build_isolation.sh

demo:
	./scripts/make-demo.sh

# Assemble a self-contained release directory: binary + docs + examples.
release: build
	rm -rf dist/13s-patcher-$(VERSION)
	mkdir -p dist/13s-patcher-$(VERSION)/examples
	cp $(BIN) dist/13s-patcher-$(VERSION)/
	cp ../version.txt dist/13s-patcher-$(VERSION)/
	cp README.md dist/13s-patcher-$(VERSION)/
	cp -r docs dist/13s-patcher-$(VERSION)/
	# the live title-footer display face and its style are read from disk at
	# build time, so they ship beside the binary for a standalone release
	cp -r assets dist/13s-patcher-$(VERSION)/
	cp examples/config.example.json examples/authoring.example.json dist/13s-patcher-$(VERSION)/examples/
	@echo "release staged in dist/13s-patcher-$(VERSION)"

fmt:
	gofmt -l -w .

clean:
	rm -rf bin dist
