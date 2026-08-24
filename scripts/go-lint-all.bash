#!/usr/bin/env bash

set -e -o pipefail

REPO_ROOT="$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )/.." &> /dev/null && pwd )"
export REPO_ROOT

lint_module() {
  local root="$1"
  shift
  cd "$(dirname "$root")" &&
    echo "linting $(grep "^module" go.mod) [$(date -Iseconds -u)]" &&
    golangci-lint run ./... -c "${REPO_ROOT}/.golangci.yml" "$@"
}
export -f lint_module

# if LINT_DIFF env is set, only lint the files in the current commit otherwise lint all files
if [[ -z "${LINT_DIFF:-}" ]]; then
  find "${REPO_ROOT}" -type f -name go.mod -print0 |
    xargs -0 -I{} bash -c 'lint_module "$@"' _ {} "$@"
else
  if [[ -z $GIT_DIFF ]]; then
    GIT_DIFF=$(git diff --name-only --diff-filter=d | grep \.go$ | grep -v \.pb\.go$) || true
  fi

  if [[ -z "$GIT_DIFF" ]]; then
    echo "no files to lint"
    exit 0
  fi

  for f in $(dirname $(echo "$GIT_DIFF" | tr -d "'") | uniq); do
    echo "linting $f [$(date -Iseconds -u)]"
    # Run in a subshell so this iteration's `cd` can never leak into the
    # next one - a failure here used to skip the "cd back to repo root"
    # step, leaving every subsequent directory resolved relative to the
    # wrong place ("no such file or directory").
    set +e
    output=$(cd "$f" && golangci-lint run ./... -c "${REPO_ROOT}/.golangci.yml" "$@" 2>&1)
    status=$?
    set -e
    echo "$output"
    if [[ $status -ne 0 ]]; then
      # A directory whose only .go file(s) carry a build tag we don't pass
      # (e.g. an e2e-only test package) has nothing for golangci-lint to
      # analyze - that's not a lint violation, so don't fail the run over it.
      if echo "$output" | grep -q "no go files to analyze"; then
        echo "skipping $f: no lintable go files for the active build tags"
        continue
      fi
      exit "$status"
    fi
  done
fi
