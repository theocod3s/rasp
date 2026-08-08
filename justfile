# rasp — task runner. Run `just` with no arguments to list every recipe.

version := `git describe --tags --always --dirty 2>/dev/null || echo dev`

# List the available recipes
default:
    @just --list

# Compile every package
build:
    go build ./...

# Build the rasp binary into ./rasp
binary:
    go build -trimpath -ldflags '-s -w -X main.version={{version}}' -o rasp ./cmd/rasp

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
    unformatted=$(gofmt -l .)
    if [ -n "$unformatted" ]; then
        echo "these files need gofmt:" >&2
        echo "$unformatted" >&2
        exit 1
    fi

# Remove build output
clean:
    rm -f rasp
    rm -rf dist

# Everything CI runs (M0-02)
ci: fmt-check vet build test race
