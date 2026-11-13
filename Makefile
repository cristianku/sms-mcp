BINARY  ?= sms-mcp
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GO      ?= go
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build test race lint fmt vet tidy clean docker

all: lint test build

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/sms-mcp

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w cmd internal

# gofmt check + go vet; staticcheck if installed.
lint: vet
	@out=$$(gofmt -l cmd internal); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi
	@if command -v staticcheck >/dev/null 2>&1; then staticcheck ./...; else echo "staticcheck not installed, skipped"; fi

tidy:
	$(GO) mod tidy

clean:
	rm -f $(BINARY)
	rm -rf dist

docker:
	docker build --build-arg VERSION=$(VERSION) -t sms-mcp:$(VERSION) .
