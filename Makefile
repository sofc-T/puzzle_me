# Build the binary
build:
	@go build -o ./bin/puzzleclient ./main.go

# Run tests
test:
	go test -v ./...

# Run the application
run: build
	@./bin/puzzleclient
