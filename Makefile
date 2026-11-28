.PHONY: runtime build generate build-quick build-cross test test-race clean

TYPX_VERSION ?= 0.7.0-dev
HASH = $(shell git rev-parse --short HEAD)
DATE = $(shell GOOS=$(shell go env GOHOSTOS) GOARCH=$(shell go env GOHOSTARCH) \
	go run tools/build-date.go)
GOBIN ?= $(shell go env GOPATH)/bin
GOVARS = -X github.com/monicafaye/TypX/internal/util.TypXVersion=$(TYPX_VERSION) -X github.com/monicafaye/TypX/internal/util.CommitHash=$(HASH) -X 'github.com/monicafaye/TypX/internal/util.CompileDate=$(DATE)'
DEBUGVAR = -X github.com/monicafaye/TypX/internal/util.Debug=ON
VSCODE_TESTS_BASE_URL = 'https://raw.githubusercontent.com/microsoft/vscode/e6a45f4242ebddb7aa9a229f85555e8a3bd987e2/src/vs/editor/test/common/model/'
CGO_ENABLED := $(if $(CGO_ENABLED),$(CGO_ENABLED),0)

ADDITIONAL_GO_LINKER_FLAGS := ""

build: generate build-quick

build-quick:
	CGO_ENABLED=$(CGO_ENABLED) go build -trimpath -ldflags "-s -w $(GOVARS) $(ADDITIONAL_GO_LINKER_FLAGS)" ./cmd/typx

build-dbg:
	CGO_ENABLED=$(CGO_ENABLED) go build -trimpath -ldflags "$(ADDITIONAL_GO_LINKER_FLAGS) $(DEBUGVAR)" ./cmd/typx

build-tags: fetch-tags build

build-all: build

build-cross: generate
	mkdir -p binaries
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w $(GOVARS)" -o binaries/typx-windows-amd64.exe ./cmd/typx
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -trimpath -ldflags "-s -w $(GOVARS)" -o binaries/typx-windows-arm64.exe ./cmd/typx
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags "-s -w $(GOVARS)" -o binaries/typx-linux-amd64 ./cmd/typx
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags "-s -w $(GOVARS)" -o binaries/typx-linux-arm64 ./cmd/typx
	CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags "-s -w $(GOVARS)" -o binaries/typx-darwin-amd64 ./cmd/typx
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags "-s -w $(GOVARS)" -o binaries/typx-darwin-arm64 ./cmd/typx

install: generate
	go install -ldflags "-s -w $(GOVARS) $(ADDITIONAL_GO_LINKER_FLAGS)" ./cmd/typx

install-all: install

fetch-tags:
	git fetch --tags --force

generate:
	GOOS=$(shell go env GOHOSTOS) GOARCH=$(shell go env GOHOSTARCH) go generate ./runtime

testgen:
	mkdir -p tools/vscode-tests
	cd tools/vscode-tests && \
	curl --remote-name-all $(VSCODE_TESTS_BASE_URL){editableTextModelAuto,editableTextModel,model.line}.test.ts
	tsc tools/vscode-tests/*.ts > /dev/null; true
	go run tools/testgen.go tools/vscode-tests/*.js > buffer_generated_test.go
	mv buffer_generated_test.go internal/buffer
	gofmt -w internal/buffer/buffer_generated_test.go

test:
	go test ./...

test-race:
	CGO_ENABLED=1 go test -race ./...

bench:
	for i in 1 2 3; do \
		go test -bench=. ./internal/...; \
	done > benchmark_results
	benchstat benchmark_results

bench-baseline:
	for i in 1 2 3; do \
		go test -bench=. ./internal/...; \
	done > benchmark_results_baseline

bench-compare:
	for i in 1 2 3; do \
		go test -bench=. ./internal/...; \
	done > benchmark_results
	benchstat -alpha 0.15 benchmark_results_baseline benchmark_results

clean:
	rm -f typx typx.exe micro micro.exe
	rm -rf binaries
