.PHONY: help build fmt tidy test lint-go check
help:
	@echo 'build fmt tidy test lint-go check'
build:
	go build -o bin/garm-provider-agent37 .
fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')
tidy:
	go mod tidy
test:
	go test -race ./...
lint-go:
	go vet ./...
check: test lint-go
