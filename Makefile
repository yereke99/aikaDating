SHELL := /bin/sh

AIR_VERSION := v1.67.1
TOOLS_DIR := $(CURDIR)/.tools/bin
AIR := $(TOOLS_DIR)/air
WEB_STAMP := web/node_modules/.package-lock.json

.PHONY: run build test tools check-env clean

run: check-env tools $(WEB_STAMP)
	set -a; . ./.env; set +a; exec $(AIR) -c .air.toml

build: $(WEB_STAMP)
	npm --prefix web run build
	mkdir -p .tmp
	go build -o .tmp/aikabot ./cmd

test:
	go test ./...
	npm --prefix web run lint

tools: $(AIR)

$(AIR):
	mkdir -p $(TOOLS_DIR)
	GOBIN=$(TOOLS_DIR) go install github.com/air-verse/air@$(AIR_VERSION)

$(WEB_STAMP): web/package.json web/package-lock.json
	npm --prefix web ci

check-env:
	@test -f .env || (echo ".env not found; copy .env.example to .env" && exit 1)

clean:
	rm -rf .tmp
