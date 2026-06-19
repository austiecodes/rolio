.PHONY: build web test loop bench-locomo bench-longmemeval

web:
	npm --prefix web ci
	npm --prefix web run build

build: web
	go build -o bin/rolio ./cmd/rolio
	go build -o bin/rolio-server ./cmd/rolio-server

test:
	go test -race ./...
	go vet ./...

# Run an agent scenario with pi against a temporary server and database.
loop:
	test/agentloop/run.sh $(SCENARIO)

# Run a public benchmark with pi. See test/bench/README.md.
bench-locomo:
	test/bench/run.sh locomo

bench-longmemeval:
	test/bench/run.sh longmemeval
