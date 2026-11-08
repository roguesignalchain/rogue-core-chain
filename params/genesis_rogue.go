package params

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/params/types/genesisT"
)

var (
	// RogueGenesisHash will be populated with the deterministic hash of the genesis block.
	RogueGenesisHash = common.Hash{}
)

// DefaultRogueGenesisBlock returns the official genesis block specification for Rogue Signal.
func DefaultRogueGenesisBlock() *genesisT.Genesis {
	return &genesisT.Genesis{
		Config:     RogueChainConfig,
		Nonce:      0,
		ExtraData:  []byte("Rogue Signal Genesis 2026"),
		GasLimit:   60000000,
		Difficulty: hexutil.MustDecodeBig("0x10000"), // Starting test difficulty (65,536)
		Timestamp:  1772928000,                       // Fixed launch epoch (Sunday, March 8, 2026)
		Alloc:      make(genesisT.GenesisAlloc),      // 0% Premine - 100% Fair Launch
	}
}
