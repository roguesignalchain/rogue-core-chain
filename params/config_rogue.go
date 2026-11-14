package params

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/params/types/coregeth"
	"github.com/ethereum/go-ethereum/params/types/ctypes"
	"github.com/ethereum/go-ethereum/params/types/goethereum"
	"github.com/ethereum/go-ethereum/params/vars"
)

var (
	// RogueTreasuryAddress is the protocol development treasury address (5% dev fee).
	RogueTreasuryAddress = common.HexToAddress("0xe9aaE535B7971A9AC8C6af0e4B524D02260D3a03")

	// RogueBootnodes are the official bootstrap discovery nodes for Rogue Signal.
	RogueBootnodes = []string{}

	// RogueChainConfig is the chain parameters to run a node on the Rogue Signal network.
	RogueChainConfig = &coregeth.CoreGethChainConfig{
		NetworkID:                 800055,
		ChainID:                   big.NewInt(800055),
		SupportedProtocolVersions: vars.DefaultProtocolVersions,
		RandomX: &ctypes.RandomXConfig{
			FastMode:             false,
			Treasury:             RogueTreasuryAddress,
			StrictMonotonicBlock: big.NewInt(0),
		},

		// Base protocol rules (Homestead, Tangerine, Spurious Dragon)
		EIP2FBlock:   big.NewInt(0),
		EIP7FBlock:   big.NewInt(0),
		EIP150Block:  big.NewInt(0),
		EIP155Block:  big.NewInt(0), // Strict replay protection from genesis
		EIP160FBlock: big.NewInt(0),
		EIP161FBlock: big.NewInt(0),
		EIP170FBlock: big.NewInt(0),

		// Byzantium
		EIP100FBlock: big.NewInt(0),
		EIP140FBlock: big.NewInt(0),
		EIP198FBlock: big.NewInt(0),
		EIP211FBlock: big.NewInt(0),
		EIP212FBlock: big.NewInt(0),
		EIP213FBlock: big.NewInt(0),
		EIP214FBlock: big.NewInt(0),
		EIP658FBlock: big.NewInt(0),

		// Constantinople & Petersburg
		EIP145FBlock:  big.NewInt(0),
		EIP1014FBlock: big.NewInt(0),
		EIP1052FBlock: big.NewInt(0),

		// Istanbul
		EIP152FBlock:  big.NewInt(0),
		EIP1108FBlock: big.NewInt(0),
		EIP1344FBlock: big.NewInt(0),
		EIP1884FBlock: big.NewInt(0),
		EIP2028FBlock: big.NewInt(0),
		EIP2200FBlock: big.NewInt(0),

		// Berlin (ModExp, cold/warm state access gas repricing)
		EIP2565FBlock: big.NewInt(0),
		EIP2929FBlock: big.NewInt(0),

		// London (refund reduction & 0xEF bytecode rejection; EIP-1559 intentionally omitted)
		EIP3529FBlock: big.NewInt(0),
		EIP3541FBlock: big.NewInt(0),

		// Shanghai (Warm COINBASE, PUSH0, initcode metering/limit, SELFDESTRUCT deprecation)
		EIP3651FBlock: big.NewInt(0),
		EIP3855FBlock: big.NewInt(0),
		EIP3860FBlock: big.NewInt(0),
		EIP6049FBlock: big.NewInt(0),

		// Cancun (MCOPY, TSTORE/TLOAD, EIP-6780 SELFDESTRUCT restriction)
		EIP5656FBlock: big.NewInt(0),
		EIP1153FBlock: big.NewInt(0),
		EIP6780FBlock: big.NewInt(0),

		// ECBP-1100 (MESS - Modified Exponential Subjective Scoring artificial finality)
		ECBP1100FBlock: big.NewInt(0),

		// Defuse difficulty bomb forever (pure PoW RandomX difficulty managed by LWMA-1)
		DisposalBlock: big.NewInt(0),

		RequireBlockHashes: map[uint64]common.Hash{
			0: RogueGenesisHash,
		},
	}
)

func randomXConfig(cfg ctypes.ChainConfigurator) *ctypes.RandomXConfig {
	switch c := cfg.(type) {
	case *coregeth.CoreGethChainConfig:
		if c != nil {
			return c.RandomX
		}
	case *goethereum.ChainConfig:
		if c != nil {
			return c.RandomX
		}
	}
	return nil
}

// ResolveRandomXTreasury returns the configured RandomX treasury address from cfg,
// falling back to RogueTreasuryAddress if unspecified.
func ResolveRandomXTreasury(cfg ctypes.ChainConfigurator) common.Address {
	if rx := randomXConfig(cfg); rx != nil && rx.Treasury != (common.Address{}) {
		return rx.Treasury
	}
	return RogueTreasuryAddress
}

// IsStrictMonotonicTimestamp reports whether strict timestamp monotonicity (header.Time > parent.Time)
// is active at blockNumber for the given RandomX chain configuration.
func IsStrictMonotonicTimestamp(cfg ctypes.ChainConfigurator, blockNumber *big.Int) bool {
	return randomXConfig(cfg).IsStrictMonotonic(blockNumber)
}

// MaxExtraDataSizeForChain returns the maximum allowed block ExtraData size in bytes
// for the given chain configuration (64 bytes for RandomX, 32 bytes otherwise).
func MaxExtraDataSizeForChain(cfg ctypes.ChainConfigurator) uint64 {
	if cfg != nil && cfg.GetConsensusEngineType().IsRandomX() {
		return vars.RogueMaximumExtraDataSize
	}
	if cfg != nil {
		if max := cfg.GetMaximumExtraDataSize(); max != nil {
			return *max
		}
	}
	return vars.MaximumExtraDataSize
}
