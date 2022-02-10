package liquidatoor

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"log"
	"math/big"
	"os"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/kargakis/liquidatoor/pkg/abis"
)

type Liquidatoor struct {
	// Node connection
	client *ethclient.Client
	// Blockchain explorer URL
	explorerURL string
	// TODO: Figure out whether it is faster to always
	// instantiate this vs deep-copying to avoid mutations
	// or whether we don't care about mutations as these
	// will always be in specific fields, ie., gas stuff
	TxOpts *bind.TransactOpts

	// Contracts
	Multicall     *abis.Multicall
	Comptroller   *abis.Comptroller
	BorrowMarkets map[string]*abis.CToken
	LendMarkets   map[string]*abis.CToken

	getAccountLiquidityMethod abi.Method
}

func validate() error {
	if os.Getenv("BLOCKCHAIN_EXPLORER_URL") == "" {
		return errors.New("BLOCKCHAIN_EXPLORER_URL cannot be empty")
	}
	if os.Getenv("COMPTROLLER_ADDRESS") == "" {
		return errors.New("COMPTROLLER_ADDRESS cannot be empty")
	}
	if os.Getenv("PRIVATE_KEY") == "" {
		return errors.New("PRIVATE_KEY cannot be empty")
	}
	if os.Getenv("MULTICALL_ADDRESS") == "" {
		return errors.New("MULTICALL_ADDRESS cannot be empty")
	}
	if os.Getenv("NODE_API_URL") == "" {
		return errors.New("NODE_API_URL cannot be empty")
	}
	return nil
}

var (
	noOpts = new(bind.CallOpts)
	zero   = big.NewInt(0)
)

func New() (*Liquidatoor, error) {
	if err := validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Instantiate liquidatoor
	l := &Liquidatoor{
		explorerURL:   os.Getenv("BLOCKCHAIN_EXPLORER_URL"),
		BorrowMarkets: make(map[string]*abis.CToken),
		LendMarkets:   make(map[string]*abis.CToken),
	}

	// Connect to node
	client, err := ethclient.Dial(os.Getenv("NODE_API_URL"))
	if err != nil {
		return nil, fmt.Errorf("cannot connect to node: %w", err)
	}
	l.client = client

	chainID, err := client.NetworkID(context.Background())
	if err != nil {
		return nil, fmt.Errorf("cannot get chain id: %w", err)
	}
	fmt.Println("Chain ID:", chainID)

	// Load private key
	privateKey, err := crypto.HexToECDSA(os.Getenv("PRIVATE_KEY"))
	if err != nil {
		return nil, fmt.Errorf("cannot load private key: %w", err)
	}

	// Extract address
	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("cannot cast public key to ECDSA")
	}
	address := crypto.PubkeyToAddress(*publicKeyECDSA)
	fmt.Println("Address:", address)

	txOpts, err := bind.NewKeyedTransactorWithChainID(privateKey, chainID)
	if err != nil {
		return nil, fmt.Errorf("cannot create authorized transactor: %w", err)
	}
	l.TxOpts = txOpts

	// Instantiate multicall contract
	multicall, err := abis.NewMulticall(common.HexToAddress(os.Getenv("MULTICALL_ADDRESS")), client)
	if err != nil {
		return nil, fmt.Errorf("cannot instantiate multicall: %w", err)
	}
	l.Multicall = multicall

	// Instantiate comptroller
	comptroller, err := abis.NewComptroller(common.HexToAddress(os.Getenv("COMPTROLLER_ADDRESS")), client)
	if err != nil {
		return nil, fmt.Errorf("cannot instantiate comptroller: %w", err)
	}
	l.Comptroller = comptroller

	// Instantiate markets
	markets, err := comptroller.GetAllMarkets(noOpts)
	if err != nil {
		return nil, fmt.Errorf("cannot get markets: %w", err)
	}

	for _, market := range markets {
		cToken, err := abis.NewCToken(market, client)
		if err != nil {
			return nil, fmt.Errorf("cannot get CToken for market %s: %w", market, err)
		}
		borrows, err := cToken.TotalBorrows(noOpts)
		if err != nil {
			return nil, fmt.Errorf("cannot read total borrows for CToken %s: %w", market, err)
		}
		if borrows.Cmp(zero) == 1 {
			l.BorrowMarkets[market.String()] = cToken
		}
		l.LendMarkets[market.String()] = cToken
	}

	abi, err := abis.ComptrollerMetaData.GetAbi()
	if err != nil {
		return nil, fmt.Errorf("cannot get comptorller ABI: %w", err)
	}
	l.getAccountLiquidityMethod = abi.Methods["getAccountLiquidity"]

	return l, nil
}

func (l *Liquidatoor) ShortfallCheck() error {
	log.Println("Starting shortfall checks...")
	borrowers, err := l.Comptroller.GetAllBorrowers(noOpts)
	if err != nil {
		return fmt.Errorf("cannot get all borrowers: %w", err)
	}
	fmt.Println("Number of borrowers:", len(borrowers))

	calls := []abis.MulticallCall{}
	id := l.getAccountLiquidityMethod.ID

	for _, borrower := range borrowers {
		inputs, err := l.getAccountLiquidityMethod.Inputs.Pack(borrower)
		if err != nil {
			return fmt.Errorf("cannot pack borrower: %w", err)
		}
		calls = append(calls, abis.MulticallCall{
			Target:   common.HexToAddress(os.Getenv("COMPTROLLER_ADDRESS")),
			CallData: append(id[:], inputs[:]...),
		})
	}

	resp, err := l.Multicall.Aggregate(noOpts, calls)
	if err != nil {
		return fmt.Errorf("failed multicall request: %v", err)
	}

	for i, data := range resp.ReturnData {
		out, err := l.getAccountLiquidityMethod.Outputs.Unpack(data)
		if err != nil {
			return fmt.Errorf("cannot unpack output: %v", err)
		}
		cErr := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)
		liquidity := *abi.ConvertType(out[1], new(*big.Int)).(**big.Int)
		shortfall := *abi.ConvertType(out[2], new(*big.Int)).(**big.Int)
		if cErr.Cmp(zero) != 0 {
			log.Printf("contract error while getting account %s liquidity: %v\n", borrowers[i], cErr)
			continue
		}
		if liquidity.Cmp(shortfall) == 1 {
			fmt.Printf("Account %s has liquidity %v\n", borrowers[i], liquidity)
		} else {
			fmt.Printf("Account %s is underwater by %v\n", borrowers[i], shortfall)
		}
	}

	return nil
}
