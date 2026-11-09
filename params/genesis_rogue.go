package params

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/params/types/genesisT"
)

var (
	// RogueGenesisHash is the deterministic canonical hash of the Rogue Signal genesis block (Block 0).
	RogueGenesisHash = common.HexToHash("0xc6f7f29d6a23f1921f3e514401a82a60ffa1f21885497dbf6414e8bb2f22b91d")
)

// DefaultRogueGenesisBlock returns the official genesis block specification for Rogue Signal.
func DefaultRogueGenesisBlock() *genesisT.Genesis {
	return &genesisT.Genesis{
		Config:     RogueChainConfig,
		Nonce:      0,
		Coinbase:   RogueTreasuryAddress,
		ExtraData:  []byte("ROGUE::800055 // THE_SILICON_BELONGS_TO_THE_MANY // 09.11.26"),
		GasLimit:   60000000,
		Difficulty: hexutil.MustDecodeBig("0x10000"), // Starting difficulty (65,536)
		Timestamp:  1762711936,                       // Fixed launch epoch (Monday, Nov 9, 2026, 0x6910d980)
		Alloc:      make(genesisT.GenesisAlloc),      // 0% Premine - 100% Fair Launch
	}
}
