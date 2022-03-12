package liquidatoor

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"log"
	"math/big"
	"os"
	"sort"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
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
	Multicall          *abis.Multicall
	Comptroller        *abis.Comptroller
	Oracle             *abis.PriceOracle
	BorrowMarkets      map[string]*abis.CToken
	LendMarkets        map[string]*abis.CToken
	comptrollerAddress common.Address
	comptrollerABI     *abi.ABI

	borrowerCacheInterval time.Duration
	borrowerCache         *BorrowerCache

	underlyingInfo map[string]UnderlyingInfo
}

var (
	noOpts = new(bind.CallOpts)
	zero   = big.NewInt(0)
)

func New() (*Liquidatoor, error) {
	// Instantiate liquidatoor
	l := &Liquidatoor{
		BorrowMarkets:  make(map[string]*abis.CToken),
		LendMarkets:    make(map[string]*abis.CToken),
		underlyingInfo: make(map[string]UnderlyingInfo),
	}

	// Run validations
	if err := l.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Connect to node
	// TODO: Make timeout configurable
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
	fmt.Printf("Liquidatoor address: %s/address/%s\n", l.explorerURL, address)

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
	comptroller, err := abis.NewComptroller(l.comptrollerAddress, client)
	if err != nil {
		return nil, fmt.Errorf("cannot instantiate comptroller: %w", err)
	}
	l.Comptroller = comptroller

	oracle, err := comptroller.Oracle(noOpts)
	if err != nil {
		return nil, fmt.Errorf("cannot fetch price oracle: %w", err)
	}
	l.Oracle, err = abis.NewPriceOracle(oracle, client)
	if err != nil {
		return nil, fmt.Errorf("cannot instantiate price oracle: %w", err)
	}

	abi, err := abis.ComptrollerMetaData.GetAbi()
	if err != nil {
		return nil, fmt.Errorf("cannot get comptroller ABI: %w", err)
	}
	l.comptrollerABI = abi

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
	if err := l.getUnderlyingInfo(); err != nil {
		return nil, err
	}

	l.prettyPrintMarkets()

	// Start borrower cache in a separate thread
	l.borrowerCache = NewBorrowerCache(l.borrowerCacheInterval, multicall, comptroller, abi)
	go l.borrowerCache.Init()

	return l, nil
}

func (l *Liquidatoor) validate() error {
	explorerURL := os.Getenv("BLOCKCHAIN_EXPLORER_URL")
	if explorerURL == "" {
		return errors.New("BLOCKCHAIN_EXPLORER_URL cannot be empty")
	}
	l.explorerURL = explorerURL

	if os.Getenv("BORROWER_CACHE_INTERVAL") == "" {
		return errors.New("BORROWER_CACHE_INTERVAL cannot be empty")
	}
	borrowerCacheInterval, err := time.ParseDuration(os.Getenv("BORROWER_CACHE_INTERVAL"))
	if err != nil {
		return err
	}
	l.borrowerCacheInterval = borrowerCacheInterval

	comptrollerAddress := os.Getenv("COMPTROLLER_ADDRESS")
	if comptrollerAddress == "" {
		return errors.New("COMPTROLLER_ADDRESS cannot be empty")
	}
	l.comptrollerAddress = common.HexToAddress(comptrollerAddress)

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

func (l *Liquidatoor) getAccountLiquidityMethod() abi.Method {
	return l.comptrollerABI.Methods["getAccountLiquidity"]
}

func (l *Liquidatoor) getUnderlyingInfo() error {
	for address, market := range l.LendMarkets {
		underlying, err := market.Underlying(noOpts)
		if err != nil {
			return fmt.Errorf("cannot get underlying: %w", err)
		}

		erc20, err := abis.NewCToken(underlying, l.client)
		if err != nil {
			return fmt.Errorf("cannot get interface for underlying %s: %w", underlying, err)
		}

		name, err := erc20.Name(noOpts)
		if err != nil {
			return fmt.Errorf("cannot get name for underlying %s: %w", underlying, err)
		}
		decimals, err := erc20.Decimals(noOpts)
		if err != nil {
			return fmt.Errorf("cannot get decimals for underlying %s: %w", underlying, err)
		}
		l.underlyingInfo[address] = UnderlyingInfo{name: name, decimals: decimals}
	}
	return nil
}

func (l *Liquidatoor) prettyPrintMarkets() {
	if len(l.LendMarkets) == 0 {
		return
	}

	cTokenABI, err := abis.CTokenMetaData.GetAbi()
	if err != nil {
		log.Printf("Failed to get ctoken ABI: %v", err)
		return
	}

	priceOracleABI, err := abis.PriceOracleMetaData.GetAbi()
	if err != nil {
		log.Printf("Failed to get price oracle ABI: %v", err)
		return
	}

	calls := []abis.MulticallCall{}
	symbolMethod := cTokenABI.Methods["symbol"]
	getPriceMethod := priceOracleABI.Methods["getUnderlyingPrice"]

	oracle, _ := l.Comptroller.Oracle(noOpts)

	for address := range l.LendMarkets {
		calls = append(calls, abis.MulticallCall{
			Target:   common.HexToAddress(address),
			CallData: symbolMethod.ID,
		})
		inputs, err := getPriceMethod.Inputs.Pack(common.HexToAddress(address))
		if err != nil {
			log.Printf("cannot pack cToken: %v", err)
			return
		}
		calls = append(calls, abis.MulticallCall{
			Target:   oracle,
			CallData: append(getPriceMethod.ID[:], inputs[:]...),
		})
	}

	resp, err := l.Multicall.Aggregate(noOpts, calls)
	if err != nil {
		log.Printf("Failed multicall request to get symbols: %v", err)
		return
	}

	fmt.Println()
	fmt.Println("MARKETS")
	for i, data := range resp.ReturnData {
		if i%2 == 0 {
			out, err := symbolMethod.Outputs.Unpack(data)
			if err != nil {
				log.Printf("Failed to unpack symbol output: %v", err)
				return
			}
			symbol := *abi.ConvertType(out[0], new(string)).(*string)
			fmt.Printf("- %s/address/%s (%s)\n", l.explorerURL, calls[i].Target, symbol)
		} else {
			out, err := getPriceMethod.Outputs.Unpack(data)
			if err != nil {
				log.Printf("Failed to unpack price output: %v", err)
				return
			}
			price := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)
			fmt.Printf("  Price: %v\n", price)
		}
	}
	fmt.Println()
}

func (l *Liquidatoor) SubscribeToBlocks() {
	headers := make(chan *types.Header)
	sub, err := l.client.SubscribeNewHead(context.Background(), headers)
	if err != nil {
		log.Fatalf("Failed to subscribe to headers: %v", err)
	}

	for {
		select {
		case err := <-sub.Err():
			log.Printf("Got subscription error: %v", err)

		case header := <-headers:
			log.Printf("Processing block %d", header.Number.Uint64())

			// TODO: Avoid processing when in-flight check is in progress
			if err := l.ShortfallCheck(); err != nil {
				log.Printf("Failed shortfall check: %v", err)
			}
		}
	}
}

func (l *Liquidatoor) ShortfallCheck() error {
	log.Println("Starting shortfall checks...")

	borrowers := l.borrowerCache.Read()
	log.Printf("Number of borrowers: %d", len(borrowers))

	if len(borrowers) == 0 {
		// Ignore if the cache is not primed yet
		log.Println("Empty borrower cache; aborting shortfall check")
		return nil
	}

	// Fetch all borrowers liquidity
	calls := []abis.MulticallCall{}
	id := l.getAccountLiquidityMethod().ID

	for _, borrower := range borrowers {
		inputs, err := l.getAccountLiquidityMethod().Inputs.Pack(borrower.Address)
		if err != nil {
			return fmt.Errorf("cannot pack borrower: %w", err)
		}
		calls = append(calls, abis.MulticallCall{
			Target:   l.comptrollerAddress,
			CallData: append(id[:], inputs[:]...),
		})
	}

	resp, err := l.Multicall.Aggregate(noOpts, calls)
	if err != nil {
		return fmt.Errorf("failed multicall request: %v", err)
	}

	// Filter underwater accounts
	underwaterAccounts := make([]Borrower, 0)
	for i, data := range resp.ReturnData {
		out, err := l.getAccountLiquidityMethod().Outputs.Unpack(data)
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
		res := liquidity.Cmp(shortfall)
		if res == -1 {
			underwaterAccounts = append(underwaterAccounts, Borrower{
				Address:   borrowers[i].Address,
				Assets:    borrowers[i].Assets,
				Shortfall: shortfall,
			})
		}
	}
	sort.Sort(ByShortfall(underwaterAccounts))

	for _, acc := range underwaterAccounts {
		fmt.Printf("Account %s is underwater by %v\n", acc.Address, acc.Shortfall)
		// TODO: Check whether it is worth to execute liquidation
		// liquidateCalculateSeizeTokens
		l.getAssets(acc.Address, acc.Assets)
	}

	log.Println("Shortfall check complete.")

	return nil
}

func (l *Liquidatoor) getAssets(account common.Address, assets []common.Address) {
	lentAssets := make([]*abis.CToken, 0)
	borrowedAssets := make([]*abis.CToken, 0)

	for _, asset := range assets {
		address := asset.String()

		underlyingInfo := l.underlyingInfo[address]
		cToken, ok := l.BorrowMarkets[address]
		if !ok {
			cToken = l.LendMarkets[address]
			lentAssets = append(lentAssets, cToken)

			balance, err := cToken.BalanceOfUnderlying(noOpts, account)
			if err != nil {
				log.Printf("Failed to get underlying balance for account %s: %v", account, err)
				return
			}
			sBalance := Balance{value: balance, decimals: underlyingInfo.decimals}
			fmt.Printf("Account %s has balance %s in %s\n", account, sBalance, underlyingInfo.name)
		} else {
			borrowedAssets = append(borrowedAssets, cToken)

			borrowed, err := cToken.BorrowBalanceStored(noOpts, account)
			if err != nil {
				log.Printf("Failed to get underlying balance for account %s: %v", account, err)
				return
			}
			// If borrowed balance is zero here than this is an asset
			// the user has lent instead of borrowed, sooo...
			if borrowed.Cmp(zero) != 0 {
				sBalance := Balance{value: borrowed, decimals: underlyingInfo.decimals}
				fmt.Printf("Account %s has borrowed balance %s in %s\n", account, sBalance, underlyingInfo.name)
				// Should be getting BalanceOfUnderlying
			}
		}
	}

}
