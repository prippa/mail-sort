# Cross-compile MailSorter. Release binaries must be built with CGO disabled.
# `go test -race` needs cgo for the race runtime only; it must not pull cgo libraries.

BINARY := mailsorter
MODULE := github.com/prippa/mail-sort
PKG := ./cmd/mailsorter
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w \
	-X $(MODULE)/internal/buildinfo.Version=$(VERSION) \
	-X $(MODULE)/internal/buildinfo.DefaultGoogleClientID=$(GOOGLE_CLIENT_ID) \
	-X $(MODULE)/internal/buildinfo.DefaultMicrosoftClientID=$(MICROSOFT_CLIENT_ID)

.PHONY: all build build-linux-amd64 build-linux-arm64 build-windows-amd64 test test-race lint vet clean

all: build

build: build-linux-amd64 build-linux-arm64 build-windows-amd64

build-linux-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-linux-amd64 $(PKG)

build-linux-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-linux-arm64 $(PKG)

build-windows-amd64:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-windows-amd64.exe $(PKG)

test:
	CGO_ENABLED=0 go test ./...

test-race:
	go test -race ./...

vet:
	CGO_ENABLED=0 go vet ./...

lint:
	golangci-lint run ./...

clean:
	rm -rf dist
