#!/usr/bin/env bash
# hbench.sh ROUNDS DIR "ARGS" label=cmd... — interleaved whole-process runs of harness binaries (native goimports)
R=$1; DIR=$2; ARGS=$3; shift 3
cd /tmp/oapiperf/src/examples/$DIR || exit 1
tmp=$(mktemp -d)
for ((r=0; r<R; r++)); do
  for spec in "$@"; do
    name=${spec%%=*}; cmd=${spec#*=}
    s=$(perl -MTime::HiRes=time -e 'printf "%.4f", time')
    env $cmd -- $ARGS > $tmp/$name.out 2>&1 || echo "FAIL $name" >&2
    grep -q "err=<nil>" $tmp/$name.out || echo "ERR $name: $(head -2 $tmp/$name.out)" >&2
    e=$(perl -MTime::HiRes=time -e 'printf "%.4f", time')
    echo "$e - $s" | bc >> $tmp/$name
  done
done
for spec in "$@"; do
  name=${spec%%=*}
  printf "%-12s median=%s  (%s)\n" $name $(sort -n $tmp/$name | awk '{a[NR]=$1}END{print a[int((NR+1)/2)]}') "$(sort -n $tmp/$name | tr '\n' ' ')"
done
