package main

import (
	"log"

	"github.com/kargakis/liquidatoor/pkg/liquidatoor"
)

func main() {
	l, err := liquidatoor.New()
	if err != nil {
		log.Fatalf("Failed to instantiate liquidatoor: %v", err)
	}

	l.SubscribeToBlocks()
}
