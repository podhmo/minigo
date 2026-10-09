#!/usr/bin/env bash
# dk.sh WORKDIR CMD... — run inside golang:1.27-alpine with the host module cache (read-only)
W=$1; shift
M="$(go env GOMODCACHE)"
exec docker run --rm -v /tmp/oapiperf:/tmp/oapiperf -v "$M:$M:ro" \
  -e GOMODCACHE="$M" -e GOPROXY=off -e GOTOOLCHAIN=local -e GOFLAGS=-mod=mod -w "$W" golang:1.27-alpine "$@"
