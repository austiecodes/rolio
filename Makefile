.PHONY: build web test

web:
	npm --prefix web ci
	npm --prefix web run build

build: web
	go build -o bin/rolio ./cmd/rolio
	go build -o bin/rolio-server ./cmd/rolio-server

test:
	go test -race ./...
	go vet ./...
