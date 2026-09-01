.PHONY: test verify demo load docker-demo

GO ?= go

test:
	$(GO) test -buildvcs=false ./...

verify:
	test -z "$$(gofmt -l .)"
	$(GO) vet -buildvcs=false ./...
	$(GO) test -buildvcs=false ./...
	$(GO) test -buildvcs=false -race ./...

demo:
	$(GO) run -buildvcs=false ./cmd/marketserver -db service-demo.db -seed-demo

load:
	$(GO) run -buildvcs=false ./cmd/marketload -duration 10s -concurrency 16

docker-demo:
	docker compose up --build
