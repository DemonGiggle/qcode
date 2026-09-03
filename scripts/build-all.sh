#!/bin/sh
set -eu

version=${VERSION:-$(sh ./scripts/version.sh)}
out=${OUT_DIR:-dist}
mkdir -p "$out"

build() {
	os=$1
	arch=$2
	suffix=$3
	name="qcode_${os}_${arch}${suffix}"
	printf 'building %s\n' "$name"
	CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
		-ldflags="-s -w -X main.version=$version" -o "$out/$name" ./cmd/qcode
}

build linux amd64 ""
build linux arm64 ""
build darwin amd64 ""
build darwin arm64 ""
build windows amd64 .exe
build windows arm64 .exe
build freebsd amd64 ""
