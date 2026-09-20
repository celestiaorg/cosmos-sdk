#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

if (( $# < 3 )); then
  echo "usage: $0 <govulncheck> <allowlist> <module>..." >&2
  exit 2
fi

GOVULNCHECK=$1
ALLOWLIST=$2
shift 2

finalec=0
for module in "$@"; do
  module_name=$(sed -n 's/^module //p' "$module/go.mod")
  echo "Running govulncheck for $module_name"

  output=$(mktemp)
  set +o errexit
  (cd "$module" && "$GOVULNCHECK" ./...) >"$output" 2>&1
  ec=$?
  set -o errexit
  cat "$output"

  if (( ec == 3 )); then
    found=0
    unallowed=0
    while IFS= read -r id; do
      found=1
      if ! grep -qx "$id" "$ALLOWLIST"; then
        echo "Unallowed vulnerability: $id" >&2
        unallowed=1
      fi
    done < <(grep -Eo 'GO-[0-9]{4}-[0-9]+' "$output" | sort -u)

    if (( found == 0 )); then
      echo "govulncheck reported vulnerabilities without a parseable ID" >&2
    elif (( unallowed == 0 )); then
      echo "Only explicitly allowed vulnerabilities found."
      ec=0
    fi
  fi

  rm -f "$output"
  if (( ec != 0 )); then
    finalec=$ec
  fi
done

exit "$finalec"
