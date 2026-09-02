BINARY := bareplane

.PHONY: build test vet fmt check

build:
	go build -o bin/$(BINARY) ./cmd/bareplane

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

check:
	test -z "$$(gofmt -l $$(find . -name '*.go' -not -path './vendor/*'))"
	go vet ./...
	go test ./...
	go build ./...
