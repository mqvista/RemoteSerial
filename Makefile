# Build targets for rsagent (Linux/macOS host). Windows hosts use build.ps1.
VERSION ?= dev
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X rsagent/internal/buildinfo.Version=$(VERSION) \
	-X rsagent/internal/buildinfo.Commit=$(COMMIT) \
	-X rsagent/internal/buildinfo.Date=$(DATE)

.PHONY: all web windows linux-arm64 linux-amd64 darwin darwin-arm64 darwin-amd64 macapp deb manifest run test clean

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

# Windows 用 GUI 子系统，双击不弹控制台；非 ui 子命令在终端里会接回父进程的控制台
windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS) -H windowsgui" -o dist/rsagent-windows-amd64.exe ./cmd/rsagent

linux-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/rsagent-linux-arm64 ./cmd/rsagent

linux-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/rsagent-linux-amd64 ./cmd/rsagent

# macOS 需要 CGO：go.bug.st/serial 的串口枚举调用 IOKit，只能在 macOS 主机上编译
darwin: darwin-arm64 darwin-amd64

darwin-arm64:
	CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 CGO_CFLAGS="-arch arm64" CGO_LDFLAGS="-arch arm64" go build -trimpath -ldflags "$(LDFLAGS)" -o dist/rsagent-darwin-arm64 ./cmd/rsagent

darwin-amd64:
	CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 CGO_CFLAGS="-arch x86_64" CGO_LDFLAGS="-arch x86_64" go build -trimpath -ldflags "$(LDFLAGS)" -o dist/rsagent-darwin-amd64 ./cmd/rsagent

# macOS .app 包（arm64 + x86_64 通用二进制，ad-hoc 签名）：make macapp VERSION=0.4.0
# 产物 dist/RemoteSerial Agent.app 以及 dist/rsagent-macos.zip
APP := dist/RemoteSerial Agent.app
APP_VERSION := $(patsubst v%,%,$(VERSION))
macapp: darwin
	rm -rf "$(APP)"
	mkdir -p "$(APP)/Contents/MacOS" "$(APP)/Contents/Resources"
	lipo -create -output "$(APP)/Contents/MacOS/rsagent" dist/rsagent-darwin-arm64 dist/rsagent-darwin-amd64
	sed 's/@VERSION@/$(if $(filter dev,$(APP_VERSION)),0.0.0,$(APP_VERSION))/g' deploy/macos/Info.plist > "$(APP)/Contents/Info.plist"
	cp deploy/macos/rsagent.icns "$(APP)/Contents/Resources/rsagent.icns"
	codesign --force --sign - "$(APP)"
	cd dist && rm -f rsagent-macos.zip && ditto -c -k --keepParent "RemoteSerial Agent.app" rsagent-macos.zip

run:
	go run ./cmd/rsagent run --console

test:
	go test ./...

clean:
	rm -rf dist internal/web/dist/assets internal/web/dist/index.html
