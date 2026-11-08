package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
)

const Banner = `
  ____                               ____  _                   _ 
 |  _ \ ___   __ _ _   _  ___       / ___|| |_ _ __ __ _  ___ | |
 | |_) / _ \ / _` + "`" + ` | | | |/ _ \ _____ \___ \| __| '__/ _` + "`" + ` |/ _ \| |
 |  _ < (_) | (_| | |_| |  __/|_____| ___) | |_| | | (_| | (_) |_|
 |_| \_\___/ \__, |\__,_|\___|      |____/ \__|_|  \__,_|\___/(_)
             |___/                                               
       Rogue Signal (ROGUE) Stratum Bridge v1.0.0
       RandomX (rx/0) PoW <-> Core-Rogue EVM Node
`

func main() {
	fmt.Print(Banner)

	cfg := LoadConfig()

	log.Printf("[Stratum] Initializing bridge for Rogue Signal (Chain ID: 800055)")
	log.Printf("[Stratum] Mining algorithm: RandomX (rx/0) | Target block time: 10s")

	server := NewStratumServer(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := server.Start(ctx); err != nil {
		log.Fatalf("Fatal error starting Stratum bridge: %v", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	sig := <-sigCh
	log.Printf("[Stratum] Received signal %v, shutting down...", sig)
	cancel()
	server.Stop()
}
