package main

import (
	"flag"
	"fmt"
	"math/big"
	"time"
)

// Config holds the configuration parameters for rogue-stratum bridge.
type Config struct {
	StratumHost string
	StratumPort int
	NodeRPCURL  string

	PollInterval time.Duration
	ShareDiff    uint64

	MaxConnections int
	LogLevel       string
}

// LoadConfig parses command-line flags and returns validated Config.
func LoadConfig() *Config {
	cfg := &Config{}

	flag.StringVar(&cfg.StratumHost, "stratum.host", "0.0.0.0", "Stratum server listen host")
	flag.IntVar(&cfg.StratumPort, "stratum.port", 3333, "Stratum server listen port")
	flag.StringVar(&cfg.NodeRPCURL, "node.rpc", "http://127.0.0.1:38845", "Rogue Signal Core-Geth HTTP RPC endpoint")
	flag.DurationVar(&cfg.PollInterval, "poll.interval", 250*time.Millisecond, "Interval for polling new work from node")
	flag.Uint64Var(&cfg.ShareDiff, "diff", 1000, "Fixed share difficulty for connected miners")
	flag.IntVar(&cfg.MaxConnections, "max.conn", 1024, "Maximum concurrent Stratum connections")
	flag.StringVar(&cfg.LogLevel, "log.level", "info", "Logging verbosity (debug, info, warn, error)")

	flag.Parse()

	if cfg.ShareDiff == 0 {
		cfg.ShareDiff = 1000
	}

	return cfg
}

// ListenAddr returns formatted host:port string.
func (c *Config) ListenAddr() string {
	return fmt.Sprintf("%s:%d", c.StratumHost, c.StratumPort)
}

// ShareDiffTarget calculates 4-byte compact Stratum target for XMRig based on share difficulty.
// For RandomX / Monero Stratum, target = 0xFFFFFFFFFFFFFFFF / Diff (upper 32-bit compact: 0xFFFFFFFF / Diff).
func ShareDiffTarget(diff uint64) (uint32, string) {
	if diff == 0 {
		diff = 1
	}
	max32 := new(big.Int).SetUint64(0xFFFFFFFF)
	t32 := new(big.Int).Div(max32, new(big.Int).SetUint64(diff))
	targetVal := uint32(t32.Uint64())
	if targetVal == 0 {
		targetVal = 1
	}

	// 4 bytes in little-endian format
	b0 := byte(targetVal & 0xFF)
	b1 := byte((targetVal >> 8) & 0xFF)
	b2 := byte((targetVal >> 16) & 0xFF)
	b3 := byte((targetVal >> 24) & 0xFF)

	targetHex := fmt.Sprintf("%02x%02x%02x%02x", b0, b1, b2, b3)
	return targetVal, targetHex
}
