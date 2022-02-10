build:
	go build ./cmd/liquidatoor
PHONY: build

generate:
	abigen --abi assets/Comptroller.json --pkg abis --type Comptroller --out pkg/abis/comptroller.go
PHONY: generate
