BIN      := bin/api
INSTALL  := $(HOME)/.local/bin/api
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILT    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w -X cybercat/api/internal/cli.Version=$(VERSION) -X cybercat/api/internal/cli.BuildTime=$(BUILT)

# CGO off: a static binary that cross-compiles cleanly (e.g. for Termux).
export CGO_ENABLED := 0

.PHONY: build install install-skill test vet fmt check lint e2e e2e-keychain e2e-secretservice smoke fuzz vuln cover release clean

build:
	go build -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/api

# Replace the file (a new inode), never overwrite it in place: on Apple silicon a
# `cp` over a binary that already ran leaves a stale cached code signature and the
# kernel kills the new bytes at launch (exit 137, no message).
install: build
	mkdir -p $(dir $(INSTALL))
	cp $(BIN) $(INSTALL).new
	mv -f $(INSTALL).new $(INSTALL)

test:
	go test -count=1 -race ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

# What to run before calling a change done.
check:
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }
	go vet ./...
	go test -count=1 -race -shuffle=on ./...

# golangci-lint when installed (go vet already runs in `make check`).
lint:
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run ./... || echo "golangci-lint not installed: skipped (make check runs gofmt + go vet + tests)"

# The real macOS Keychain, with one throwaway item that is removed again.
e2e-keychain:
	API_TEST_KEYCHAIN=1 go test -count=1 -v -run TestKeychain ./internal/secretstore

# The real Secret Service via secret-tool, one throwaway item (Linux desktop session).
e2e-secretservice:
	API_TEST_SECRETSERVICE=1 go test -count=1 -v -run 'TestSecretService$$' ./internal/secretstore

# Shell shim end to end (needs python3 and localhost ports; run outside a sandbox).
e2e: build
	zsh test/shim.zsh

# Fuzz every target for FUZZTIME each (their seed corpora also run in `make check`).
# A failure writes the input to testdata/fuzz/: keep it, it becomes a regression test.
FUZZTIME ?= 10s
FUZZ_TARGETS := config:FuzzSetInFile config:FuzzParseText httpfile:FuzzParse httpfile:FuzzInterpolateNeverLoopsForever \
	envfile:FuzzExampleNeverContainsValues jwt:FuzzParse render:FuzzPathEvaluation secretref:FuzzParse \
	schema:FuzzValidLabelStaysInsideTheCache schema:FuzzParse cli:FuzzSplitWords capture:FuzzWalkBody curlimport:FuzzParseRender

fuzz:
	@for t in $(FUZZ_TARGETS); do \
		pkg=$${t%%:*}; fn=$${t##*:}; echo "== $$pkg $$fn"; \
		go test ./internal/$$pkg -run '^$$' -fuzz "^$$fn\$$" -fuzztime $(FUZZTIME) || exit 1; \
	done

# Known vulnerabilities in the dependencies AND in the standard library this was
# built with. Needs the network; not part of `make check`. Pin the tool version.
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.1.4

vuln:
	go run $(GOVULNCHECK) ./...

COVER := cover.out

cover:
	go test -count=1 -coverprofile=$(COVER) ./...
	@go tool cover -func=$(COVER) | tail -n 1
	@echo "per-function report: go tool cover -func=$(COVER) | sort -k3 -n | head -30"

# Static binaries for the machines this runs on, with checksums, in dist/.
RELEASE_TARGETS := darwin/arm64 darwin/amd64 linux/arm64 linux/amd64

release:
	rm -rf dist && mkdir -p dist
	@for t in $(RELEASE_TARGETS); do \
		os=$${t%%/*}; arch=$${t##*/}; out=dist/api-$(VERSION)-$$os-$$arch; \
		echo "building $$out"; \
		GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags '$(LDFLAGS)' -o $$out ./cmd/api || exit 1; \
	done
	cd dist && shasum -a 256 api-* > SHA256SUMS && cat SHA256SUMS

# The Claude Code skill that teaches an agent to drive `api`. User level by default;
# for one project only: make install-skill SKILL_DIR=/path/to/project/.claude/skills/api-cli
# Replace the file, never overwrite it in place (same rule as the binary).
SKILL_DIR ?= $(HOME)/.claude/skills/api-cli
install-skill:
	mkdir -p $(SKILL_DIR)
	cp skills/api-cli.md $(SKILL_DIR)/SKILL.md.new
	mv -f $(SKILL_DIR)/SKILL.md.new $(SKILL_DIR)/SKILL.md
	@echo "✓ $(SKILL_DIR)/SKILL.md"

# Whole tool end to end: real binary + shim, two stub servers, throwaway Keychain items
# (api-smoke-*, token.smoke-*; removed afterwards). Needs python3, jq, localhost ports.
smoke: build
	zsh test/smoke.zsh

clean:
	rm -rf bin dist $(COVER)
