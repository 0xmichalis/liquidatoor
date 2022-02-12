package liquidatoor

import (
	"fmt"
	"math/big"
)

type Balance struct {
	value    *big.Int
	decimals uint8
}

var (
	divider6  = big.NewInt(1000000)
	divider8  = big.NewInt(100000000)
	divider9  = big.NewInt(1000000000)
	divider18 = big.NewInt(1000000000000000000)
)

func (b Balance) String() string {
	switch b.decimals {
	case 6:
		return new(big.Int).Div(b.value, divider6).String()
	case 8:
		return new(big.Int).Div(b.value, divider8).String()
	case 9:
		return new(big.Int).Div(b.value, divider9).String()
	case 18:
		return new(big.Int).Div(b.value, divider18).String()
	default:
		panic(fmt.Sprintf("no support for %d decimals", b.decimals))
	}
}
