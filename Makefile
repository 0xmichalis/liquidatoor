include .env
export $(shell sed 's/=.*//' .env)

build:
	go build ./cmd/liquidatoor
PHONY: build

generate:
	abigen --abi assets/Comptroller.json --pkg abis --type Comptroller --out pkg/abis/comptroller.go
	abigen --abi assets/CToken.json --pkg abis --type CToken --out pkg/abis/ctoken.go
	abigen --abi assets/Multicall.json --pkg abis --type Multicall --out pkg/abis/multicall.go
	abigen --abi assets/PriceOracle.json --pkg abis --type PriceOracle --out pkg/abis/price_oracle.go
PHONY: generate

run:
	./liquidatoor
.PHONY: run
