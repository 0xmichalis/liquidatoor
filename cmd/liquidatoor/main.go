package main

import (
	"log"
	"time"

	"github.com/kargakis/liquidatoor/pkg/liquidatoor"
)

func main() {
	l, err := liquidatoor.New()
	if err != nil {
		log.Fatalf("Failed to instantiate liquidatoor: %v", err)
	}

	// TODO: Change to every block
	c := time.Tick(10 * time.Second)
	for range c {
		if err := l.ShortfallCheck(); err != nil {
			log.Printf("Failed to check for shortfall event: %v", err)
		}
	}
}
