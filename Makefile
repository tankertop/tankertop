BINARY  := tankertop
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GOFLAGS := -trimpath
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: build install run test vet tidy dist clean

build: ## build the binary for the host platform
	CGO_ENABLED=0 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) .

install: ## go install into $GOBIN
	CGO_ENABLED=0 go install $(GOFLAGS) -ldflags "$(LDFLAGS)" .

run: build ## build then run
	./$(BINARY)

test: ## run unit tests
	go test ./...

vet: ## go vet
	go vet ./...

tidy: ## sync go.mod/go.sum
	go mod tidy

dist: ## cross-compile all release targets into dist/
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build $(GOFLAGS) -ldflags "$(LDFLAGS)" \
			-o dist/$(BINARY)-$$os-$$arch$$ext . ; \
	done

clean: ## remove build artifacts
	rm -rf $(BINARY) dist/
