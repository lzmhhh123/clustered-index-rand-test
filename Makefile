all: build

build: fmt
	@echo "Building binary..."
	@go build -o bin/clustered-index-rand-test

fmt:
	@echo "go fmt..."
	@go fmt ./...

abtest: bins build
	@./tests/run-test.sh

bins:
	@which bin/tidb-master || (echo "bin/tidb-master not found" && exit 1)
	@which bin/tidb-4.0 || (echo "bin/tidb-4.0 not found" && exit 1)

test-syntax: bins build
	@python3 tests/run-syntax-check.py

count ?= 20
gen: build
	@bin/clustered-index-rand-test print --count $(count) | xargs -I _ echo "_;"

#clean:
#	@rm bin/tidb-master 2> /dev/null || echo "bin/tidb-master not found"
#	@rm bin/tidb-4.0 2> /dev/null || echo "bin/tidb-4.0 not found"
#	@rm bin/clustered-index-rand-test 2> /dev/null || echo "bin/tidb-4.0 not found"

stop-services:
	@./tests/_utils/stop_services
