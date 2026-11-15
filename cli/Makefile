GO ?= go
VERSION ?= dev
GOVULNCHECK_VERSION := v1.9.0

.PHONY: build fmt fmt-check vet test integration-test security check cross-build release release-check

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/cairn ./cmd/cairn

fmt:
	gofmt -w cmd internal schema

fmt-check:
	@test -z "$$(gofmt -l cmd internal schema)" || { gofmt -l cmd internal schema; exit 1; }

vet:
	$(GO) vet ./...

test:
	CAIRN_TEST_BORG= $(GO) test -race ./...

integration-test:
	@test -n "$(CAIRN_TEST_BORG)" || { echo "CAIRN_TEST_BORG must point to an executable Borg binary" >&2; exit 1; }
	@test -x "$(CAIRN_TEST_BORG)" || { echo "CAIRN_TEST_BORG is not executable: $(CAIRN_TEST_BORG)" >&2; exit 1; }
	CAIRN_TEST_BORG="$(CAIRN_TEST_BORG)" $(GO) test -race -count=1 -v ./...

security:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

check: fmt-check vet test security

cross-build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/cairn-linux-amd64 ./cmd/cairn
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/cairn-linux-arm64 ./cmd/cairn

release:
	@test -n "$(VERSION)" || { echo "VERSION must be set (for example 0.1.0)" >&2; exit 1; }
	./scripts/package-release.sh "$(VERSION)" "$(or $(OUT_DIR),dist)"

release-check:
	@tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	  ./scripts/package-release.sh 0.0.0-test "$$tmp"; \
	  ./scripts/test-release.sh "$$tmp" 0.0.0-test
