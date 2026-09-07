APP := qcode
VERSION ?= $(shell sh ./scripts/version.sh)
GO ?= go
BUILD_FLAGS := -trimpath -ldflags=-s\ -w\ -X\ main.version=$(VERSION)

.PHONY: build test eval release clean

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build $(BUILD_FLAGS) -o bin/$(APP) ./cmd/qcode

test:
	$(GO) test ./...

eval: build
	cd tools/qcode-tester && $(GO) test ./...
	cd tools/qcode-tester && $(GO) run ./cmd/qcode-tester --qcode-bin ../../bin/qcode

release:
	VERSION=$(VERSION) ./scripts/build-all.sh

clean:
	rm -rf bin dist
