#!/usr/bin/env bash
set -euo pipefail

go_version="$(go env GOVERSION)"
if [ "$go_version" != go1.26.5 ]; then
  echo "Expected go1.26.5, got $go_version" >&2
  exit 1
fi
python -c 'import sys; assert sys.version_info[:3] == (3, 11, 18), sys.version'
node -e 'if (process.version !== "v22.23.5") throw new Error(`Expected v22.23.5, got ${process.version}`)'
npm --version
(cd semantic-map/go && go test ./pkg/types)
(cd semantic-map/sdk/python && python -m unittest -v test_di_collector)
node --test .github/scripts/ci-triage.test.mjs
