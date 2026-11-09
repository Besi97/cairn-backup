.DEFAULT_GOAL := check

# Do not silently download a different Go toolchain for a check or tool.
export GOTOOLCHAIN := local

STATICCHECK_VERSION := v0.8.2
GOVULNCHECK_VERSION := v1.9.0

.PHONY: check fmt-check vet staticcheck security test build-all

check: fmt-check vet staticcheck security test build-all

fmt-check:
	@files="$$(gofmt -l .)" || exit $$?; \
	if [ -n "$$files" ]; then \
		printf 'Run gofmt on:\n%s\n' "$$files"; \
		exit 1; \
	fi

vet:
	go vet ./...

staticcheck:
	go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...

security:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

test:
	go test -race ./...

build-all:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o bin/linux-amd64/cairn ./cmd/cairn
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o bin/linux-arm64/cairn ./cmd/cairn
