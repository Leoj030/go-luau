.PHONY: build test test-v vet fmt sync example clean

build:
	go build ./...

# -count=1 bypasses Go's test cache, so tests always actually run
test:
	go test ./tests/... -count=1

test-v:
	go test ./tests/... -v -count=1

vet:
	go vet ./...

fmt:
	gofmt -l -w luau tests examples

# Re-copy Luau sources from the submodule (only after updating it)
sync:
	sh sync-luau.sh

example:
	go run ./examples/basic

clean:
	rm -f go-luau