# Crayfish Makefile — Na everybody food.
# Pure Go, no CGo, single static binary per platform.

BINARY    := crayfish
# Version: use git tag if available, env override, or default to dev
VERSION   ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo "0.4.0-dev")
COMMIT    := $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")
BUILD_TIME := $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
OAUTH_PKG := github.com/KekwanuLabs/crayfish/internal/oauth
LDFLAGS   := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildTime=$(BUILD_TIME)
ifdef GOOGLE_CLIENT_ID
LDFLAGS   += -X '$(OAUTH_PKG).CrayfishClientID=$(GOOGLE_CLIENT_ID)' -X '$(OAUTH_PKG).CrayfishClientSecret=$(GOOGLE_CLIENT_SECRET)'
endif
GOFLAGS   := -trimpath

# All release targets: linux (4 archs) + macOS (2 archs)
TARGETS := linux-armv6 linux-armv7 linux-arm64 linux-amd64 darwin-amd64 darwin-arm64

.PHONY: all build run $(addprefix build-,$(TARGETS)) build-all \
        test bench clean install lint vet fmt \
        check-size check-size-all help release deploy deploy-clean

# ==================================================================
# Build
# ==================================================================

all: build

build:
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/crayfish/

build-linux-armv6:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=6 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BINARY)-linux-armv6 ./cmd/crayfish/

build-linux-armv7:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BINARY)-linux-armv7 ./cmd/crayfish/

build-linux-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BINARY)-linux-arm64 ./cmd/crayfish/

build-linux-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BINARY)-linux-amd64 ./cmd/crayfish/

build-darwin-amd64:
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BINARY)-darwin-amd64 ./cmd/crayfish/

build-darwin-arm64:
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BINARY)-darwin-arm64 ./cmd/crayfish/

build-all: $(addprefix build-,$(TARGETS))
	@echo "Built all targets:"
	@ls -lh $(BINARY)-*

build-linux: build-linux-armv6 build-linux-armv7 build-linux-arm64 build-linux-amd64

release: build-all check-size-all

# ==================================================================
# Test & Quality
# ==================================================================

test:
	go test -v -race -count=1 ./...

bench:
	go test -bench=. -benchmem ./...

lint:
	go vet ./...
	@which staticcheck > /dev/null 2>&1 && staticcheck ./... || true

fmt:
	gofmt -s -w .

vet:
	go vet ./...

# ==================================================================
# Run (local development)
# ==================================================================

run: build
	./$(BINARY)

# ==================================================================
# Deploy (dev workflow: MacBook → Pi, one command)
# ==================================================================

deploy:
	@bash scripts/deploy.sh

# Fresh deploy: wipe all data on Pi, then deploy
deploy-clean:
	@bash scripts/deploy.sh --clean

# ==================================================================
# Install
# ==================================================================

install: build
	sudo cp $(BINARY) /usr/local/bin/
	sudo chmod 755 /usr/local/bin/$(BINARY)

clean:
	rm -f $(BINARY) $(addprefix $(BINARY)-,$(TARGETS))

# ==================================================================
# Size checks (binary must be < 20MB)
# ==================================================================

check-size: build
	@SIZE=$$(stat -f%z $(BINARY) 2>/dev/null || stat -c%s $(BINARY) 2>/dev/null); \
	SIZE_MB=$$((SIZE / 1024 / 1024)); \
	echo "Binary size: $$SIZE_MB MB ($$SIZE bytes)"; \
	if [ $$SIZE_MB -ge 20 ]; then echo "FAIL: exceeds 20MB!"; exit 1; \
	else echo "PASS: within budget."; fi

check-size-all:
	@for f in $(addprefix $(BINARY)-,$(TARGETS)); do \
		if [ -f $$f ]; then \
			SIZE=$$(stat -f%z $$f 2>/dev/null || stat -c%s $$f 2>/dev/null); \
			SIZE_MB=$$((SIZE / 1024 / 1024)); \
			printf "  %-28s %3d MB\n" "$$f" "$$SIZE_MB"; \
			if [ $$SIZE_MB -ge 20 ]; then echo "  ^ FAIL!"; fi; \
		fi; \
	done

# ==================================================================
# Help
# ==================================================================

help:
	@echo "Crayfish — Accessible AI for Everyone"
	@echo ""
	@echo "Development:"
	@echo "  make build        Build for current platform"
	@echo "  make run          Build and run locally"
	@echo "  make test         Run tests"
	@echo "  make lint         Run linters"
	@echo ""
	@echo "Deploy to Pi:"
	@echo "  make deploy       Build + push + restart on your Pi"
	@echo "  make deploy-clean Wipe Pi data, then deploy fresh"
	@echo ""
	@echo "Release:"
	@echo "  make build-all    Cross-compile all 6 platforms"
	@echo "  make clean        Remove build artifacts"
	@echo ""
	@echo "  make help         This message"
