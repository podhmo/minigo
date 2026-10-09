#!/usr/bin/env bash
# run1.sh BIN DIR ARGS... — run one go:generate line under minigo (or native when BIN ends in oapi-native); prints wall seconds
BIN="$1"; DIR="$2"; shift 2
cd /tmp/oapiperf/src/examples/"$DIR" || exit 1
SRC="--src text/template,encoding/json,encoding/hex,encoding/base64,encoding/base32,slices,maps"
s=$(perl -MTime::HiRes=time -e 'printf "%.3f", time')
case "$BIN" in
*oapi-native) "$BIN" "$@" ;;
*) "$BIN" run $SRC github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen -- "$@" ;;
esac > /tmp/oapiperf/last.log 2>&1
rc=$?
e=$(perl -MTime::HiRes=time -e 'printf "%.3f", time')
echo "$DIR rc=$rc $(echo "$e - $s" | bc)"
