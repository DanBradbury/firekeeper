default:
    @just --list

# Run Firekeeper.
run:
    go run .

# Run portable block renderer.
run-blocks:
    go run . --renderer blocks

# Run Kitty graphics renderer.
run-kitty:
    go run . --renderer kitty

# Build local binary.
build:
    go build -o firekeeper .

# Install Firekeeper with Go.
install:
    go install .

# Run all tests.
test:
    go test ./...

# Run tests with race detection.
race:
    go test -race ./...

# Run Go vet.
vet:
    go vet ./...

# Format Go sources.
fmt:
    gofmt -w $(git ls-files '*.go')

# Check Go formatting without changing files.
fmt-check:
    test -z "$(gofmt -l $(git ls-files '*.go'))"

# Check whitespace errors.
diff-check:
    git diff --check

# Run standard pre-commit checks.
check: test vet fmt-check diff-check
