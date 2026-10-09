#!/usr/bin/env bash
# withsrc.sh EXTRA BIN args... — rewrite the first --src list to add EXTRA packages
extra=$1; bin=$2; shift 2
args=()
for a in "$@"; do
  if [ "$prev" = "--src" ]; then a="$a,$extra"; fi
  args+=("$a"); prev=$a
done
exec "$bin" "${args[@]}"
