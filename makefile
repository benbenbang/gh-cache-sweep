.EXPORT_ALL_VARIABLES:
NAME = gh-cache-sweep
DirName ?= build
PKG = main
ProjectUrl = "https://github.com/benbenbang/gh-cache-sweep"
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
BuildTime = $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
BuildCommit = $(shell git rev-parse --short HEAD)
DEFAULT_CORES = 1
TREE_LEVEL ?= 5
NIGHTLY ?= 0
ARGS ?=
TEST_FLAGS ?= -race -cover
LDFLAGS = -s -w -X '$(PKG).Version=$(VERSION)' -X '$(PKG).BuildTime=$(BuildTime)' -X '$(PKG).ProjectUrl=$(subst ",,$(ProjectUrl))'
EXE = $(if $(filter windows,$(GOOS)),.exe,)

# Determine the OS and ARCH if not provided
UNAME_S := $(shell uname -s)
UNAME_M := $(shell uname -m)

# ifdef NIGHTLY
ifeq ($(NIGHTLY),1)
    VERSION = $(shell git rev-parse --short HEAD)
    VERSION_TYPE = "nightly"
else
    VERSION = $(shell git describe --abbrev=0 --tags 2>/dev/null || echo $(shell git rev-parse --short HEAD))
    VERSION_TYPE = "latest release"
endif

# count cpu
ifeq ($(UNAME_S),Darwin)
    DEFAULT_CORES := $(shell sysctl -n hw.ncpu)
else
    DEFAULT_CORES := $(shell nproc)
endif

CORES ?= ${DEFAULT_CORES}

# Default OS and ARCH
ifeq ($(UNAME_S), Darwin)
	DEFAULT_GOOS = darwin
else ifeq ($(UNAME_S), Linux)
	DEFAULT_GOOS = linux
else ifeq ($(UNAME_S), Windows)
	DEFAULT_GOOS = windows
else
	DEFAULT_GOOS = $(UNAME_S)
endif

# Determine the ARCH if not provided
ifeq ($(UNAME_M), x86_64)
	DEFAULT_GOARCH = amd64
else ifeq ($(UNAME_M), arm64)
	DEFAULT_GOARCH = arm64
else
	DEFAULT_GOARCH = $(UNAME_M)
endif

.PHONY: verify
## Verify that NAME is defined
verify:
	@if [ -z "$(NAME)" ]; then \
		echo "Error: NAME is not defined. Please set NAME (e.g., make build NAME=obj_transform)"; \
		exit 1; \
	fi

.PHONY: build-platform
## Build for specified OS and ARCH
build-platform: verify
	@echo "Building $(VERSION_TYPE) version: $(VERSION)"
	@mkdir -p $(DirName)
	@GOOS=$(GOOS) GOARCH=$(GOARCH) go build -p $(CORES) -v \
	        -o ./${DirName}/$(NAME)-$(GOOS)-$(GOARCH) \
		    -ldflags="$(LDFLAGS)" \
		    . && \
	chmod +x ./${DirName}/$(NAME)-$(GOOS)-$(GOARCH)
	@echo "Built $(NAME) for $(GOOS) $(GOARCH)"

.PHONY: build
## Build for all supported platforms and architectures
build: verify
	@$(MAKE) build-platform GOOS=darwin GOARCH=amd64
	@$(MAKE) build-platform GOOS=darwin GOARCH=arm64
	@$(MAKE) build-platform GOOS=linux GOARCH=amd64
	@$(MAKE) build-platform GOOS=linux GOARCH=arm64
	@$(MAKE) build-platform GOOS=windows GOARCH=amd64
	@$(MAKE) build-platform GOOS=windows GOARCH=arm64
	@echo "Built $(NAME) for all platforms"

.PHONY: check fmt vet test run version build-local install
## Format Go source, then run static analysis
check: fmt
	@$(MAKE) --no-print-directory vet
## Format Go source
fmt:
	go fmt ./...

## Run Go static analysis
vet:
	go vet ./...

## Run tests (override TEST_FLAGS or pass ARGS for focused tests)
test:
	go test $(TEST_FLAGS) $(ARGS) ./...

## Run from source with build metadata (pass CLI flags via ARGS)
run:
	go run -ldflags="$(LDFLAGS)" . $(ARGS)

## Print injected build metadata without calling GitHub
version:
	@$(MAKE) --no-print-directory run ARGS=--version

## Build the extension binary at the checkout root
build-local: verify
	go build -p $(CORES) -ldflags="$(LDFLAGS)" -o $(NAME)$(EXE) .

## Build and install the local gh extension (checkout must be named gh-cache-sweep)
install: build-local
	gh extension install .

.PHONY: clean
## Remove build files and caches
clean:
	@rm -rf ./$(DirName) 2>/dev/null || true
	@rm -f coverage.html coverage.out 2>/dev/null || true
	@go clean -cache
	@go clean -testcache
	@echo "Cleaned build artifacts and caches"

.PHONY: tree
## Print file structure
tree:
	@tree . -I 'artifacts' -I 'vendor' -I 'templates' -I 'logs' -I 'stacks' -I 'scripts' -I 'build' -L $(TREE_LEVEL)

.PHONY: bintest
## Copy the specified binary file to /usr/local/bin for testing
bintest: verify
	@ls ./$(DirName)/${NAME}-${GOOS}-${GOARCH} >/dev/null 2>&1 || (echo "binary doesn't exist!" && exit 1)
	@chmod +x ./${DirName}/${NAME}-${GOOS}-${GOARCH} # && echo "ensure binary is executable"
	@if [[ -f /usr/local/bin/${NAME} ]]; then sudo rm /usr/local/bin/${NAME}; fi
	@sudo cp ./${DirName}/${NAME}-${GOOS}-${GOARCH} /usr/local/bin/${NAME}
	@echo completion file is ready at /usr/local/bin/${NAME}

.PHONY: binrm
## Remove the binary
binrm: verify
	@rm -rf  /usr/local/bin/${NAME}


.DEFAULT_GOAL := help

.PHONY: help
help:
	@echo "Available rules:"
	@awk '/^## / { description = substr($$0, 4); next } \
		/^[a-zA-Z0-9_-]+:/ { if (description != "") { split($$0, target, ":"); printf "  %-19s %s\n", target[1], description }; description = "" }' $(MAKEFILE_LIST)
