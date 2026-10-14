# Build targets for rsagent (Linux/macOS host). Windows hosts use build.ps1.
VERSION ?= dev
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X rsagent/internal/buildinfo.Version=$(VERSION) \
	-X rsagent/internal/buildinfo.Commit=$(COMMIT) \
	-X rsagent/internal/buildinfo.Date=$(DATE)

.PHONY: all web windows linux-arm64 linux-amd64 deb manifest run test clean

all: web windows linux-arm64

# deb 包，需要 nfpm（https://nfpm.goreleaser.com）：make deb VERSION=0.4.0 ARCH=arm64
ARCH ?= arm64
deb: linux-$(ARCH)
	mkdir -p build/nfpm && cp dist/rsagent-linux-$(ARCH) build/nfpm/rsagent
	ARCH=$(ARCH) VERSION=$(VERSION) nfpm package -f deploy/nfpm.yaml -p deb -t dist/
	rm -rf build/nfpm

# 自更新清单与校验和：make manifest VERSION=0.4.0 [BASE_URL=https://...]
BASE_URL ?= https://github.com/mqvista/RemoteSerial/releases/download/v$(VERSION)
manifest:
	go run ./tools/mkmanifest -version $(VERSION) -dir dist -base $(BASE_URL)

web:
	cd web && (test -f package-lock.json && npm ci || npm install)
	rm -rf internal/web/dist/assets
	cd web && npm run build

windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/rsagent-windows-amd64.exe ./cmd/rsagent

linux-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/rsagent-linux-arm64 ./cmd/rsagent

linux-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/rsagent-linux-amd64 ./cmd/rsagent

run:
	go run ./cmd/rsagent run --console

test:
	go test ./...

clean:
	rm -rf dist internal/web/dist/assets internal/web/dist/index.html
