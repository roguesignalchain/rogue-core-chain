package params

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/params/types/genesisT"
)

var (
	// RogueGenesisHash is the deterministic canonical hash of the Rogue Signal genesis block (Block 0).
	RogueGenesisHash = common.HexToHash("0xf30f172028b07950de58d2cb4d65593793c183fcd0f86c9c24c5d4a037b490b7")
)

// DefaultRogueGenesisBlock returns the official genesis block specification for Rogue Signal.
func DefaultRogueGenesisBlock() *genesisT.Genesis {
	return &genesisT.Genesis{
		Config:     RogueChainConfig,
		Nonce:      0,
		Coinbase:   RogueTreasuryAddress,
		ExtraData:  []byte("ROGUE::800055 // THE_SILICON_BELONGS_TO_THE_MANY // 09.11.26"),
		GasLimit:   50000000,
		Difficulty: hexutil.MustDecodeBig("0x10000"), // Starting difficulty (65,536)
		Timestamp:  1794247936,                       // Fixed launch epoch (Sunday, Nov 9, 2026, 0x6af20d00)
		Alloc:      make(genesisT.GenesisAlloc),      // 0% Premine - 100% Fair Launch
	}
}
