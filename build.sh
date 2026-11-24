#!/bin/sh
# Build apteva-node. With no arguments: a local binary in bin/.
# `./build.sh pi` also produces dist/apteva-node-<version>-linux-arm64.tar.gz
# with everything deploy/install.sh needs.
set -eu
cd "$(dirname "$0")"

version=$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' package.json)
ldflags="-s -w -X main.version=$version"
export GOWORK=off CGO_ENABLED=0

bun run build:ui

go build -ldflags "$ldflags" -o bin/apteva-node ./cmd/apteva-node
go build -o bin/node-sim ./cmd/node-sim
echo "bin/apteva-node $version"

if [ "${1:-}" = "pi" ]; then
  stage=dist/apteva-node-$version-linux-arm64
  rm -rf "$stage" && mkdir -p "$stage"
  GOOS=linux GOARCH=arm64 go build -ldflags "$ldflags" -o "$stage/apteva-node" ./cmd/apteva-node
  cp deploy/* node.example.yaml "$stage/"
  COPYFILE_DISABLE=1 tar -C dist -czf "$stage.tar.gz" "$(basename "$stage")"
  echo "$stage.tar.gz"
fi
