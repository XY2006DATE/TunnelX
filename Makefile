.PHONY: all build build-go build-web build-macos build-deb clean test deps run-server run-client help

all: build

build: build-web build-go

build-go:
	@mkdir -p bin
	go build -trimpath -o bin/tunnelx-server ./server
	go build -trimpath -o bin/tunnelx-client ./client

build-web:
	cd server/serverdashboard && npm ci && npm run build
	cd client/clientdashboard && npm ci && npm run build

build-macos:
	./build-macos.sh

build-deb:
	./build-deb.sh

clean:
	rm -rf bin/ dist/

test:
	go test ./...

deps:
	go mod download

run-server:
	go run ./server server/server.yaml

run-client:
	go run ./client client/client.yaml

help:
	@echo "Available targets:"
	@echo "  build       Build dashboards, server, and client"
	@echo "  build-go    Build server and client using committed web assets"
	@echo "  build-web   Rebuild embedded React dashboards"
	@echo "  build-macos Build Client/Server .app and .dmg bundles on macOS"
	@echo "  build-deb   Build Client/Server .deb packages"
	@echo "  clean       Remove local build and release artifacts"
	@echo "  test        Run the Go test suite"
	@echo "  deps        Download Go dependencies"
	@echo "  run-server  Run the server with server/server.yaml"
	@echo "  run-client  Run the client with client/client.yaml"
