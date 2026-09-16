SHELL := bash
.SHELLFLAGS := -euo pipefail -c

MODULE   := github.com/akenhq/aken
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BINARIES := aken aken-mcp aken-relay
TARGETS  := linux/amd64 linux/arm64 darwin/arm64
DIST     ?= dist

export CGO_ENABLED := 0
BUILDFLAGS := -trimpath -buildvcs=false
LDFLAGS    := -s -w -buildid= -X $(MODULE)/internal/buildinfo.version=$(VERSION)

.PHONY: build test vet lint depcheck release sums render npm test-install repro check clean

build:
	@mkdir -p bin
	@for b in $(BINARIES); do \
	  go build $(BUILDFLAGS) -ldflags '$(LDFLAGS)' -o bin/$$b ./cmd/$$b; \
	done

test:
	go test ./...

vet:
	go vet ./...

lint:
	golangci-lint run ./...

# Dependency budget (DEPENDENCIES.md): the collector may link golang.org/x/term and its dependency
# golang.org/x/sys besides the standard library; the relay may link the AWS SDK and x/time.
depcheck:
	@check() { \
	  deps=$$(go list -deps -f '{{if not .Standard}}{{.ImportPath}}{{end}}' $$1 | sed '/^$$/d' | grep -v '^$(MODULE)/' | grep -Ev "$$2" || true); \
	  if [ -n "$$deps" ]; then echo "$$1 links packages outside its budget:"; echo "$$deps"; exit 1; fi; \
	}; \
	check ./cmd/aken '^golang.org/x/(term|sys)(/|$$)'; \
	check ./cmd/aken-relay '^(github.com/aws/(aws-sdk-go-v2|smithy-go)|golang.org/x/time)(/|$$)'
	@echo "depcheck: ok"

release:
	@mkdir -p $(DIST)
	@for t in $(TARGETS); do \
	  os=$${t%/*}; arch=$${t#*/}; \
	  for b in $(BINARIES); do \
	    GOOS=$$os GOARCH=$$arch go build $(BUILDFLAGS) -ldflags '$(LDFLAGS)' -o $(DIST)/$${b}_$${os}_$${arch} ./cmd/$$b; \
	  done; \
	done

sums:
	cd "$(DIST)"; assets=(aken_* aken-mcp_* aken-relay_*); \
	  for script in install.sh run.sh install-mcp.sh; do \
	    if [[ -f "$$script" ]]; then assets+=("$$script"); fi; \
	  done; \
	  sha256sum "$${assets[@]}" > SHA256SUMS

render:
	bash packaging/render.sh "$(DIST)" "$(VERSION)" install > "$(DIST)/install.sh"
	bash packaging/render.sh "$(DIST)" "$(VERSION)" once > "$(DIST)/run.sh"
	bash packaging/render.sh "$(DIST)" "$(VERSION)" mcp > "$(DIST)/install-mcp.sh"

npm:
	bash packaging/npm.sh "$(DIST)" "$(VERSION)" "$(DIST)-npm"

test-install:
	bash packaging/install_test.sh
	bash packaging/install_mcp_test.sh
	bash packaging/npm_test.sh

# Build every release target twice in separate directories and require identical hashes.
repro:
	@rm -rf $(DIST)-repro-a $(DIST)-repro-b
	@$(MAKE) --no-print-directory release DIST=$(DIST)-repro-a
	@$(MAKE) --no-print-directory release DIST=$(DIST)-repro-b
	@diff <(cd $(DIST)-repro-a && sha256sum *) <(cd $(DIST)-repro-b && sha256sum *) && echo "repro: identical"
	@rm -rf $(DIST)-repro-a $(DIST)-repro-b

check: lint vet test depcheck repro test-install

clean:
	rm -rf bin $(DIST) $(DIST)-npm $(DIST)-repro-a $(DIST)-repro-b
