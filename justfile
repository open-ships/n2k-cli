golangci_lint_version := "v2.12.0"
secure_go_toolchain := "go1.26.5"
govulncheck_version := "v1.5.0"
gosec_version := "v2.27.1"

# list available recipes
default:
    @just --list

# ensure development tools are installed at the pinned versions
setup:
    @if command -v golangci-lint >/dev/null && golangci-lint --version 2>&1 | grep -q "{{trim_start_match(golangci_lint_version, "v")}}"; then \
        echo "golangci-lint {{golangci_lint_version}} already installed"; \
    else \
        echo "Installing golangci-lint {{golangci_lint_version}}..."; \
        curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/HEAD/install.sh | sh -s -- -b "$(go env GOPATH)/bin" {{golangci_lint_version}}; \
    fi
    @if command -v govulncheck >/dev/null && govulncheck -version 2>&1 | grep -q "govulncheck@{{govulncheck_version}}"; then \
        echo "govulncheck {{govulncheck_version}} already installed"; \
    else \
        echo "Installing govulncheck {{govulncheck_version}}..."; \
        go install golang.org/x/vuln/cmd/govulncheck@{{govulncheck_version}}; \
    fi
    @if command -v gosec >/dev/null && go version -m "$(command -v gosec)" 2>&1 | grep -q "github.com/securego/gosec/v2[[:space:]]*{{gosec_version}}"; then \
        echo "gosec {{gosec_version}} already installed"; \
    else \
        echo "Installing gosec {{gosec_version}}..."; \
        go install github.com/securego/gosec/v2/cmd/gosec@{{gosec_version}}; \
    fi

# build the n2k CLI to bin/n2k
build:
    mkdir -p bin
    go build -trimpath -o bin/n2k ./cmd/n2k

# install n2k into Go's configured bin directory
install:
    go install ./cmd/n2k

# run all tests
test:
    go test ./...

# run tests with the race detector
test-race:
    go test -race ./...

# format all Go source files
fmt:
    gofmt -w .

# run go vet
vet:
    go vet ./...

# run golangci-lint
lint:
    golangci-lint run ./...

# run vulnerability scanning and static security analysis
secure:
    GOTOOLCHAIN={{secure_go_toolchain}} govulncheck ./...
    GOTOOLCHAIN={{secure_go_toolchain}} gosec -exclude-dir=.claude -exclude=G115 ./...

# tidy Go module dependencies
tidy:
    go mod tidy
