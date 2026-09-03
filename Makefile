APP := qcode
VERSION ?= $(shell sh ./scripts/version.sh)
GO ?= go
BUILD_FLAGS := -trimpath -ldflags=-s\ -w\ -X\ main.version=$(VERSION)

.PHONY: build test release clean

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build $(BUILD_FLAGS) -o bin/$(APP) ./cmd/qcode

test:
	$(GO) test ./...

release:
	VERSION=$(VERSION) ./scripts/build-all.sh

clean:
	rm -rf bin dist
