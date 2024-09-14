# list available receipes
@default:
  just --list

# lint go files
# go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.61.0

lint:
  golangci-lint run -v

# run regular golang tests
test-go:
  go test -v -cover ./...

@_clean-yaegi:
  rm -rf /tmp/yaegi*

# run tests via yaegi
test-yaegi: && _clean-yaegi
  #!/usr/bin/env bash
  set -euox

  TMP=$(mktemp -d yaegi.XXXXXX -p /tmp)
  WRK="${TMP}/go/src/github.com/daemonp"
  mkdir -p ${WRK}
  ln -s `pwd` "${WRK}"
  cd "${WRK}/$(basename `pwd`)"
  env GOPATH="${TMP}/go" yaegi test -v .

# lint and test
test: lint test-go test-yaegi

