package randomx

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/crypto"
)

var (
	// GamificationSystemAddress is the Block-0 system contract address holding on-chain miner PoW reputation.
	GamificationSystemAddress = common.HexToAddress("0x0000000000000000000000000000000000000020")

	// gamificationBlocksMinedSlot is storage slot 1 in RogueMinerReputation (_blocksMined mapping).
	// Slot 0 is reserved by OpenZeppelin Ownable._owner.
	gamificationBlocksMinedSlot = common.BigToHash(big.NewInt(1))
)

// MinerStorageKey computes the 32-byte Solidity-standard mapping slot key for _blocksMined[miner] at slot 1:
// keccak256(abi.encode(miner, uint256(1))).
func MinerStorageKey(miner common.Address) common.Hash {
	return crypto.Keccak256Hash(
		common.LeftPadBytes(miner.Bytes(), 32),
		gamificationBlocksMinedSlot.Bytes(),
	)
}

// IncrementMinerBlockCount records an additional mined block in StateDB under GamificationSystemAddress.
func IncrementMinerBlockCount(state *state.StateDB, miner common.Address) uint64 {
	if state == nil {
		return 0
	}
	slot := MinerStorageKey(miner)
	raw := state.GetState(GamificationSystemAddress, slot)
	current := new(big.Int).SetBytes(raw.Bytes()).Uint64()
	next := current + 1

	newVal := common.BigToHash(new(big.Int).SetUint64(next))
	state.SetState(GamificationSystemAddress, slot, newVal)
	return next
}

// GetMinerBlockCount retrieves the number of canonical blocks mined by the specified address from StateDB.
func GetMinerBlockCount(state *state.StateDB, miner common.Address) uint64 {
	if state == nil {
		return 0
	}
	slot := MinerStorageKey(miner)
	raw := state.GetState(GamificationSystemAddress, slot)
	return new(big.Int).SetBytes(raw.Bytes()).Uint64()
}
