.PHONY: lint test vendor clean

export GO111MODULE=on

default: fmt lint test

fmt:
	gofumpt -l -w .
	goimports -l -w .

lint:
	golangci-lint run

test:
	go clean -testcache
	go test -v -cover ./...

yaegi_test:
	yaegi test -v .

vendor:
	go mod vendor

clean:
	rm -rf ./vendor

#
## Set environment variables
#export FORKLIFT_DEFAULT_BACKEND=http://localhost:8082
#export FORKLIFT_DEBUG=true
#
## Create a sample configuration file
#echo "defaultBackend: http://localhost:8083" > /etc/traefik/forklift.yaml
#
## Run your application or tests
#go test ./...
