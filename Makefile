CGO_CFLAGS=-DSQLITE_ENABLE_FTS5
CGO_LDFLAGS=-lm

# macOS doesn't need -lm (it's part of the system)
UNAME_S := $(shell uname -s)
ifeq ($(UNAME_S),Darwin)
  CGO_LDFLAGS=
endif

# Single source of truth for the version. Build injects it into main.Version
# via -ldflags so the binary, plugin manifests, and goreleaser tags stay in
# lockstep. Bumping the release is `echo X.Y.Z > VERSION && make sync-version`.
VERSION := $(shell cat VERSION)
LDFLAGS := -X main.Version=$(VERSION)

.PHONY: build test lint clean sync-version eval

build:
	CGO_CFLAGS="$(CGO_CFLAGS)" CGO_LDFLAGS="$(CGO_LDFLAGS)" go build -ldflags "$(LDFLAGS)" -o bin/anchored ./cmd/anchored/

test:
	CGO_CFLAGS="$(CGO_CFLAGS)" CGO_LDFLAGS="$(CGO_LDFLAGS)" go test ./... -v

# Local evaluation gates (recall, sync-safety, identity). Builds the binary and
# runs each eval against its embedded fixture; any failure exits non-zero so CI
# can gate on it.
eval: build
	./bin/anchored eval recall
	./bin/anchored eval sync-safety
	./bin/anchored eval identity

lint:
	CGO_CFLAGS="$(CGO_CFLAGS)" CGO_LDFLAGS="$(CGO_LDFLAGS)" golangci-lint run ./...

clean:
	rm -rf bin/

sync-version:
	go run ./cmd/version-sync
