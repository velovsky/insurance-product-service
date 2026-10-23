.PHONY: generate lint test run mcp build docker

generate: ## Regenerate Go code from the protobuf API
	buf generate

lint: ## Lint the API and the Go code
	buf lint
	buf format -d --exit-code
	test -z "$$(gofmt -l .)"
	go vet ./...

test: ## Run all tests with the race detector
	go test -race -count=1 ./...

run: ## Start the server with a seeded demo product on :50051
	go run ./cmd/server -seed

mcp: ## Start the MCP server for AI agents (needs a running server)
	go run ./cmd/mcp

build:
	go build -o bin/server ./cmd/server
	go build -o bin/mcp ./cmd/mcp

docker:
	docker build -t insurance-product-service .
