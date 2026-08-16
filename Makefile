.PHONY: test build run docker linux-amd64 linux-arm64 binaries

test:
	go test ./...
	go vet ./...

build:
	mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/psoc .

linux-amd64:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/psoc-linux-amd64 .

linux-arm64:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/psoc-linux-arm64 .

binaries: linux-amd64 linux-arm64

run: build
	./bin/psoc serve

docker: build
	docker build -t psoc:local .
