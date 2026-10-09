#!/usr/bin/env bash
cd /tmp/oapiperf/wt && go build -o /tmp/oapiperf/minigo-bisect ./cmd/minigo || exit 125
cd /tmp/oapiperf/src/examples/only-models && /tmp/oapiperf/minigo-bisect run --src text/template,encoding/json,encoding/hex,encoding/base64,encoding/base32,slices,maps github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen -- -config cfg.yaml api.yaml >/tmp/oapiperf/bisect.log 2>&1
