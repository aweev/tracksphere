#!/bin/bash
# Runs go tooling with an explicit PATH (go is not on the default non-login PATH).
export PATH="/usr/local/go/bin:/root/go/bin:$PATH"
cd /root/apps/tracksphere || exit 1
case "$1" in
  build) go build ./... && go vet ./... ;;
  test)  shift; go test "${@:-./...}" -count=1 ;;
  fmt)   gofmt -l . ;;
  *)     echo "usage: go-gate.sh {build|test|fmt} [go-test-args]" >&2; exit 2 ;;
esac
