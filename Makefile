APP := qcode
VERSION ?= $(shell sh ./scripts/version.sh)
GO ?= go
AGG ?= agg
BUILD_FLAGS := -trimpath -ldflags=-s\ -w\ -X\ main.version=$(VERSION)

.PHONY: build test eval release clean demo-gif

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build $(BUILD_FLAGS) -o bin/$(APP) ./cmd/qcode

test:
	$(GO) test ./...

eval: build
	cd tools/qcode-tester && $(GO) test ./...
	cd tools/qcode-tester && $(GO) run ./cmd/qcode-tester --qcode-bin ../../bin/qcode

# Requires agg (https://github.com/asciinema/agg). Keep the demo in an
# explicit color theme so ANSI colors render consistently on every refresh.
demo-gif:
	$(AGG) --theme monokai --font-size 14 --fps-cap 10 --idle-time-limit 1 docs/assets/demo.cast docs/assets/demo.gif

release:
	VERSION=$(VERSION) ./scripts/build-all.sh

clean:
	rm -rf bin dist
