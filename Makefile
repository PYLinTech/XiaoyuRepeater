VERSION ?= 0.2.0
BINARY ?= bin/xiaoyurepeater
GO ?= go

.PHONY: all build web deps run test test-race fmt docker clean

all: build

deps:
	cd web && npm ci

web: deps
	cd web && npm run build

build: web
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath \
		-ldflags "-s -w -X xiaoyurepeater/internal/version.Version=$(VERSION)" \
		-o $(BINARY) .

run:
	$(GO) run .

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

fmt:
	$(GO) fmt ./...
	cd web && npx vue-tsc --noEmit

docker:
	docker build --build-arg VERSION=$(VERSION) -t xiaoyurepeater:$(VERSION) .

clean:
	rm -rf bin
	rm -rf web/dist
