#!/usr/bin/env bash
# build.sh MINIGO_DIR OUT — build the prof harness against a minigo checkout
set -e
d=$(mktemp -d)
here=$(cd "$(dirname "$0")" && pwd)
cp "${SRCMAIN:-$here/main.go.in}" $d/main.go
cd $d
cat > go.mod <<EOM
module oapiprof

go 1.27

require github.com/podhmo/minigo v0.0.0
replace github.com/podhmo/minigo => $1
EOM
go mod tidy >/dev/null 2>&1 || go mod tidy
go build -o "$2" .
