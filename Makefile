.PHONY: generate lint test run build docker

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

build:
	go build -o bin/server ./cmd/server

docker:
	docker build -t insurance-product-service .
