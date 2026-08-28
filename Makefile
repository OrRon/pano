BIN      := bin/pano
MODULE   := github.com/orron/pano
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w -X $(MODULE)/internal/cli.version=$(VERSION) -X $(MODULE)/internal/cli.commit=$(COMMIT) -X $(MODULE)/internal/cli.date=$(DATE)
export CGO_ENABLED=0

.PHONY: build install test race lint fuzz bench bench-proxy cover tidy clean release-snapshot

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/pano

install:
	go install -trimpath -ldflags '$(LDFLAGS)' ./cmd/pano

test:
	go vet ./...
	go test -race -count=1 ./...

race: test

lint:
	golangci-lint run ./...

fuzz:
	@for pkg in $$(go list ./internal/...); do \
		for t in $$(go test -list 'Fuzz.*' $$pkg 2>/dev/null | grep '^Fuzz'); do \
			echo "fuzz $$pkg $$t"; go test -run '^$$' -fuzz "^$$t$$$$" -fuzztime 20s $$pkg || exit 1; \
		done; \
	done

bench:
	go test -run '^$$' -bench . -benchmem ./internal/...

bench-proxy: build
	bench/run.sh

cover:
	go test -race -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

tidy:
	go mod tidy
	go mod verify

clean:
	rm -rf bin dist coverage.out

release-snapshot:
	goreleaser release --snapshot --clean
