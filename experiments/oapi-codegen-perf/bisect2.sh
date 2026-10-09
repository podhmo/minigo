#!/usr/bin/env bash
cd /tmp/oapiperf/wt2 || exit 125
git apply /tmp/oapiperf/fix.patch 2>/dev/null; applied=$?
go build -o /tmp/oapiperf/minigo-b2 ./cmd/minigo; brc=$?
[ $applied -eq 0 ] && git apply -R /tmp/oapiperf/fix.patch
[ $brc -eq 0 ] || exit 125
cd /tmp/oapiperf/src/examples/extensions/xenumnames && /tmp/oapiperf/minigo-b2 run --src text/template,encoding/json,encoding/hex,encoding/base64,encoding/base32,slices,maps github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen -- -config cfg.yaml api.yaml >/tmp/oapiperf/b2.log 2>&1
