# Handy shortcuts. Everything here is plain `go` underneath.

# Go stamps the version from git: a tag like v0.1.0 on a tagged commit,
# "dev" otherwise. VERSION=v0.1.0 overrides it.
VERSION ?=
LDFLAGS := -s -w $(if $(VERSION),-X github.com/staltz/fabrica/internal/cli.version=$(VERSION))

.PHONY: build test race lint dist clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o fabrica ./cmd/fabrica

test:
	go test ./...

race:
	go test -race ./...

lint:
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...
	@test -z "$$(gofmt -l .)" || (echo "These files need gofmt:"; gofmt -l .; exit 1)

# Static binaries for every platform we release, in dist/.
dist:
	@mkdir -p dist
	@for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do \
		os=$${target%/*}; arch=$${target#*/}; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" \
			-o dist/fabrica-$$os-$$arch ./cmd/fabrica || exit 1; \
	done
	@cd dist && (command -v sha256sum >/dev/null && sha256sum fabrica-* || shasum -a 256 fabrica-*) > checksums.txt

clean:
	rm -rf fabrica dist
