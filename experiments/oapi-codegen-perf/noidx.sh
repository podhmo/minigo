#!/usr/bin/env bash
# noidx.sh CMD... — run with an empty HOME so goimports finds no module index.
# GOPATH/GOMODCACHE/GOENV/GOCACHE are read before HOME changes (GOENV may
# contain a space on macOS, hence the quoting).
export GOPATH="$(go env GOPATH)" GOMODCACHE="$(go env GOMODCACHE)" GOENV="$(go env GOENV)" GOCACHE="$(go env GOCACHE)"
mkdir -p /tmp/oapiperf/home
HOME=/tmp/oapiperf/home exec "$@"
