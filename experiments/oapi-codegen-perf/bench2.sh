#!/usr/bin/env bash
# bench.sh ROUNDS DIR "ARGS" label=cmd... — interleaved rounds of whole-process runs; prints median per label
# a label's cmd may start with VAR=val assignments (run via env).
R=$1; DIR=$2; ARGS=$3; shift 3
cd /tmp/oapiperf/src/examples/$DIR || exit 1
SRC="--src text/template,encoding/json,encoding/hex,encoding/base64,encoding/base32,slices,maps${EXTRA_SRC}"
PKG=github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen
declare -a labels
for spec in "$@"; do labels+=("$spec"); done
tmp=$(mktemp -d)
for ((r=0; r<R; r++)); do
  for spec in "${labels[@]}"; do
    name=${spec%%=*}; cmd=${spec#*=}
    s=$(perl -MTime::HiRes=time -e 'printf "%.4f", time')
    /tmp/oapiperf/noidx.sh env $cmd run $SRC $PKG -- $ARGS > /dev/null 2>&1 || echo "FAIL $name" >&2
    e=$(perl -MTime::HiRes=time -e 'printf "%.4f", time')
    echo "$e - $s" | bc >> $tmp/$name
  done
done
for spec in "${labels[@]}"; do
  name=${spec%%=*}
  printf "%-12s median=%s  (%s)\n" $name $(sort -n $tmp/$name | awk '{a[NR]=$1}END{print a[int((NR+1)/2)]}') "$(sort -n $tmp/$name | tr '\n' ' ')"
done
