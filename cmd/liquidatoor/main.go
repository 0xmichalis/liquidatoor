package main

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/kargakis/liquidatoor/pkg/abis"
)

func validate() error {
	if os.Getenv("COMPTROLLER_ADDRESS") == "" {
		return errors.New("COMPTROLLER_ADDRESS cannot be empty")
	}
	if os.Getenv("PRIVATE_KEY") == "" {
		return errors.New("PRIVATE_KEY cannot be empty")
	}
	if os.Getenv("NODE_API_URL") == "" {
		return errors.New("NODE_API_URL cannot be empty")
	}
	return nil
}

func main() {
	if err := validate(); err != nil {
		log.Fatalf("Failed to validate config: %v", err)
	}

	// Connect to node
	client, err := ethclient.Dial(os.Getenv("NODE_API_URL"))
	if err != nil {
		log.Fatalf("Failed to connect to node: %v", err)
	}
	chainID, err := client.NetworkID(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("Chain ID:", chainID)

	// Load private key
	privateKey, err := crypto.HexToECDSA(os.Getenv("PRIVATE_KEY"))
	if err != nil {
		log.Fatalf("Failed to load private key: %v", err)
	}

	// Extract address
	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		log.Fatal("error casting public key to ECDSA")
	}
	address := crypto.PubkeyToAddress(*publicKeyECDSA)
	fmt.Println("Address:", address)

	// Instantiate contract bindings
	c, err := abis.NewComptroller(common.HexToAddress(os.Getenv("COMPTROLLER_ADDRESS")), client)
	if err != nil {
		log.Fatalf("Failed to instantiate a Compotroller contract: %v", err)
	}

	markets, err := c.GetAllMarkets(&bind.CallOpts{})
	if err != nil {
		log.Fatalf("Failed to get all borrowers: %v", err)
	}
	fmt.Println("Markets:")
	for i, market := range markets {
		fmt.Printf("%d: https://polygonscan.com/address/%s\n", i+1, market)
	}

	borrowers, err := c.GetAllBorrowers(&bind.CallOpts{})
	if err != nil {
		log.Fatalf("Failed to get all borrowers: %v", err)
	}
	fmt.Println("Number of borrowers:", len(borrowers))

	for _, borrower := range borrowers {
		_ = borrower
		// TODO: Move to a multicall
		// cErr, liquidity, shortfall, err := c.GetAccountLiquidity(&bind.CallOpts{}, borrower)
		// if err != nil {
		// 	log.Fatalf("Failed to get account liquidity: %v", err)
		// }
		// fmt.Println("Borrower:", borrower)
		// fmt.Println("Contract error:", cErr)
		// fmt.Println("Liquidity:", liquidity)
		// fmt.Println("Shortfall:", shortfall)
	}

	// auth, err := bind.NewKeyedTransactorWithChainID(privateKey, chainID)
	// if err != nil {
	// 	log.Fatalf("Failed to create authorized transactor: %v", err)
	// }
}
