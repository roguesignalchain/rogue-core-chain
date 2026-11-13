package params

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/params/types/genesisT"
)

var (
	// RogueGenesisHash is the deterministic canonical hash of the Rogue Signal genesis block (Block 0).
	RogueGenesisHash = common.HexToHash("0xde4c5e747fbb072d39db2ece5a245e46b6164045e166301dfdba78f86cfbe4dd")

	// RogueReputationContractAddress is the protocol-level system contract address for PoW miner reputation.
	RogueReputationContractAddress = common.HexToAddress("0x0000000000000000000000000000000000000020")
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
		Alloc: genesisT.GenesisAlloc{ // 0% Premine - 100% Fair Launch (system contract only)
			RogueReputationContractAddress: {
				Balance: big.NewInt(0),
				Nonce:   1,
				Code:    RogueReputationRuntimeBytecode,
				Storage: map[common.Hash]common.Hash{
					// Slot 0: OpenZeppelin Ownable._owner initialized to RogueTreasuryAddress
					{}: common.BytesToHash(RogueTreasuryAddress.Bytes()),
				},
			},
		},
	}
}
