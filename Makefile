.PHONY: test build run docker

test:
	go test ./...
	go vet ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/psoc .

run:
	go run . serve

docker:
	docker build -t psoc:local .
