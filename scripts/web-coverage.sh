#!/usr/bin/env sh
set -eu

case "${WOMS_COVERAGE_POLICY:-short-term}" in
  short-term)
    min_coverage="${WOMS_WEB_COVERAGE_MIN:-40.0}"
    ;;
  medium-term|long-term)
    min_coverage="${WOMS_WEB_COVERAGE_MIN:-95.0}"
    ;;
  *)
    echo "Unknown WOMS_COVERAGE_POLICY=${WOMS_COVERAGE_POLICY}. Use short-term, medium-term, or long-term." >&2
    exit 1
    ;;
esac

mkdir -p coverage
set +e
coverage_output="$(node --test --experimental-test-coverage --test-reporter=tap --test-reporter-destination=stdout --test-reporter=lcov --test-reporter-destination=coverage/lcov.info web/*.test.mjs deploy/helm/woms/*.test.mjs deploy/argocd/*.test.mjs scripts/*.test.mjs 2>&1)"
status="$?"
set -e

printf '%s\n' "$coverage_output"
printf '%s\n' "$coverage_output" > coverage/web-coverage.txt

if [ "$status" -ne 0 ]; then
  exit "$status"
fi

node ./scripts/merge-lcov.mjs coverage/lcov.info

actual_coverage="$(printf '%s\n' "$coverage_output" | awk -F '|' '/# all files/ {gsub(/ /, "", $2); print $2}')"
if [ -z "$actual_coverage" ]; then
  echo "Unable to determine web line coverage from Node coverage output." >&2
  exit 1
fi

awk -v actual="$actual_coverage" -v required="$min_coverage" 'BEGIN {
  if ((actual + 0) < (required + 0)) {
    printf "Web line coverage %.2f%% is below required %.2f%%.\n", actual, required > "/dev/stderr"
    exit 1
  }
  printf "Web line coverage %.2f%% meets required %.2f%%.\n", actual, required
}'
