# rasp — task runner. Run `just` with no arguments to list every recipe.

version := `git describe --tags --always --dirty 2>/dev/null || echo dev`

# List the available recipes
default:
    @just --list

# Compile every package
build:
    go build ./...

# Build the rasp binary into ./rasp, shaped like a release build
binary:
    CGO_ENABLED=0 go build -trimpath -ldflags '-s -w -X main.version={{version}}' -o rasp ./cmd/rasp

# Run the tests
test:
    go test ./...

# Run the tests under the race detector
race:
    go test -race ./...

# Report suspicious constructs
vet:
    go vet ./...

# Rewrite every file with gofmt
fmt:
    gofmt -w .

# Fail if any file needs gofmt
fmt-check:
    #!/usr/bin/env sh
    set -e                      # a gofmt that cannot run must not report success
    unformatted=$(gofmt -l .)
    if [ -n "$unformatted" ]; then
        echo "these files need gofmt:" >&2
        echo "$unformatted" >&2
        exit 1
    fi

# Fail if go.mod or go.sum needs `go mod tidy`, without rewriting either
tidy-check:
    go mod tidy -diff

# Remove build output
clean:
    rm -f rasp
    rm -rf dist

# Check the OpenAI-compatible adapter against live endpoints — see the README
verify-openaicompat:
    RASP_LIVE=1 go test -count=1 -v ./internal/llm/openaicompat/

# Everything CI runs
ci: fmt-check tidy-check vet build test race

# Compile for one $GOOS/$GOARCH target: the release binary, then every package
cross-compile: binary
    CGO_ENABLED=0 go build ./...

# Build the whole release matrix into ./dist without tagging or publishing
snapshot:
    goreleaser release --snapshot --clean --skip=publish
