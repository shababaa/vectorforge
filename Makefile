.PHONY: test verify fuzz benchmark demo load docker-demo

GO ?= go

test:
	$(GO) test -buildvcs=false ./...

universe:
	$(GO) test -buildvcs=false ./market -run TestFiveYearUniverseAcceptance -count=1 -v

verify:
	test -z "$$(gofmt -l .)"
	$(GO) vet -buildvcs=false ./...
	$(GO) test -buildvcs=false ./...
	$(GO) test -buildvcs=false -race ./...

fuzz:
	$(GO) test -buildvcs=false ./ -run '^$$' -fuzz '^FuzzCodecRoundTrip$$' -fuzztime=5s
	$(GO) test -buildvcs=false ./ -run '^$$' -fuzz '^FuzzBTreeStateMachine$$' -fuzztime=5s
	$(GO) test -buildvcs=false ./ -run '^$$' -fuzz '^FuzzParserNeverPanics$$' -fuzztime=5s

benchmark:
	$(GO) test -buildvcs=false ./ -run '^$$' -bench '^BenchmarkBTreeSequentialInsert$$' -benchmem -count=3 -benchtime=20x
	$(GO) test -buildvcs=false ./market -run '^$$' -bench 'BenchmarkCandle(Ingestion|RangeScan)$$' -benchmem -count=3 -benchtime=5x

demo:
	$(GO) run -buildvcs=false ./cmd/marketserver -db service-demo.db -seed-demo

load:
	$(GO) run -buildvcs=false ./cmd/marketload -duration 10s -concurrency 16

docker-demo:
	docker compose up --build
