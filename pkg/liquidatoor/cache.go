package liquidatoor

import (
	"fmt"
	"log"
	"math/big"
	"os"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"

	"github.com/kargakis/liquidatoor/pkg/abis"
)

type Borrower struct {
	Address   common.Address
	Assets    []common.Address
	Shortfall *big.Int
}

type BorrowerCache struct {
	interval time.Duration

	lock      *sync.RWMutex
	borrowers []Borrower

	multicall          *abis.Multicall
	comptrollerAddress common.Address
	comptroller        *abis.Comptroller
	comptrollerABI     *abi.ABI
}

func NewBorrowerCache(
	interval time.Duration,
	multicall *abis.Multicall,
	comptroller *abis.Comptroller,
	comptrollerABI *abi.ABI,
) *BorrowerCache {
	return &BorrowerCache{
		interval: interval,

		lock:      &sync.RWMutex{},
		borrowers: make([]Borrower, 0),

		multicall:          multicall,
		comptrollerAddress: common.HexToAddress(os.Getenv("COMPTROLLER_ADDRESS")),
		comptroller:        comptroller,
		comptrollerABI:     comptrollerABI,
	}
}

func (c *BorrowerCache) Init() {
	if err := c.run(); err != nil {
		log.Printf("Failed to prime borrower cache: %v", err)
	}
	for range time.Tick(c.interval) {
		if err := c.run(); err != nil {
			log.Printf("Failed to update borrower cache: %v", err)
		}
	}
}

func (c *BorrowerCache) run() error {
	log.Print("Initiating a borrower cache update...")

	borrowers, err := c.comptroller.GetAllBorrowers(noOpts)
	if err != nil {
		return fmt.Errorf("cannot get all borrowers: %w", err)
	}

	calls := []abis.MulticallCall{}
	method := c.comptrollerABI.Methods["getAssetsIn"]

	for _, borrower := range borrowers {
		inputs, err := method.Inputs.Pack(borrower)
		if err != nil {
			return fmt.Errorf("cannot pack borrower: %w", err)
		}
		calls = append(calls, abis.MulticallCall{
			Target:   c.comptrollerAddress,
			CallData: append(method.ID[:], inputs[:]...),
		})
	}

	resp, err := c.multicall.Aggregate(noOpts, calls)
	if err != nil {
		return fmt.Errorf("failed multicall request: %v", err)
	}

	newBorrowers := make([]Borrower, len(borrowers))
	for i, data := range resp.ReturnData {
		out, err := method.Outputs.Unpack(data)
		if err != nil {
			return fmt.Errorf("cannot unpack output: %v", err)
		}
		assets := *abi.ConvertType(out[0], new([]common.Address)).(*[]common.Address)
		newBorrowers[i] = Borrower{Address: borrowers[i], Assets: assets}
	}

	c.lock.Lock()
	c.borrowers = newBorrowers
	c.lock.Unlock()

	log.Print("Borrower cache update complete.")
	return nil
}

func (c *BorrowerCache) Read() []Borrower {
	borrowers := make([]Borrower, len(c.borrowers))

	c.lock.RLocker().Lock()
	for i := range c.borrowers {
		borrowers[i] = Borrower{
			Address: c.borrowers[i].Address,
			Assets:  c.borrowers[i].Assets,
		}
	}
	c.lock.RLocker().Unlock()

	return borrowers
}
