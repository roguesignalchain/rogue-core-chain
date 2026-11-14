package randomx

import (
	"encoding/binary"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/crypto"
)

var (
	// GamificationSystemAddress is the Block-0 system contract address holding on-chain miner PoW reputation.
	GamificationSystemAddress = common.HexToAddress("0x0000000000000000000000000000000000000020")
)

// MinerStorageKey computes the 32-byte Solidity-standard mapping slot key for _blocksMined[miner] at slot 1:
// keccak256(abi.encode(miner, uint256(1))).
func MinerStorageKey(miner common.Address) common.Hash {
	var buf [64]byte
	copy(buf[12:32], miner[:])
	buf[63] = 1
	return crypto.Keccak256Hash(buf[:])
}

// IncrementMinerBlockCount records an additional mined block in StateDB under GamificationSystemAddress.
func IncrementMinerBlockCount(state *state.StateDB, miner common.Address) uint64 {
	if state == nil {
		return 0
	}
	slot := MinerStorageKey(miner)
	raw := state.GetState(GamificationSystemAddress, slot)
	next := binary.BigEndian.Uint64(raw[24:32]) + 1

	var newVal common.Hash
	binary.BigEndian.PutUint64(newVal[24:32], next)
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
	return binary.BigEndian.Uint64(raw[24:32])
}
