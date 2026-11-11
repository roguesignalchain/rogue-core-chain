package randomx

import (
	"math"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/crypto"
)

var (
	// GamificationSystemAddress is the designated protocol system address holding miner levels and stats.
	GamificationSystemAddress = common.HexToAddress("0x0000000000000000000000000000000000000020")
)

// MinerStats represents the calculated on-chain gamification status for a miner address.
type MinerStats struct {
	Address         common.Address `json:"address"`
	BlocksMined     uint64         `json:"blocksMined"`
	Level           uint32         `json:"level"`
	Title           string         `json:"title"`
	CurrentXP       uint64         `json:"currentXP"`
	NextLevelXP     uint64         `json:"nextLevelXP"`
	ProgressPercent float64        `json:"progressPercent"`
}

// MinerStorageKey computes the 32-byte storage slot key in GamificationSystemAddress for a miner.
func MinerStorageKey(miner common.Address) common.Hash {
	return crypto.Keccak256Hash(miner.Bytes())
}

// IncrementMinerBlockCount records an additional mined block in the StateDB under GamificationSystemAddress.
func IncrementMinerBlockCount(state *state.StateDB, miner common.Address) uint64 {
	// Ensure GamificationSystemAddress is not purged as empty by EIP-158 / Spurious Dragon state clearing.
	// An account with Nonce > 0 is non-empty and will safely persist in the StateDB across block commits.
	if state.GetNonce(GamificationSystemAddress) == 0 {
		state.SetNonce(GamificationSystemAddress, 1)
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

// CalculateMinerStats computes level, title, current XP, target XP, and progress percentage.
func CalculateMinerStats(addr common.Address, blocks uint64) MinerStats {
	stats := MinerStats{
		Address:     addr,
		BlocksMined: blocks,
	}

	switch {
	case blocks == 0:
		stats.Level = 0
		stats.Title = "Unranked Runner"
		stats.CurrentXP = 0
		stats.NextLevelXP = 1
		stats.ProgressPercent = 0.0

	case blocks < 25:
		stats.Level = 1
		stats.Title = "Recon Runner"
		stats.CurrentXP = blocks
		stats.NextLevelXP = 25
		stats.ProgressPercent = roundPercent(float64(blocks-1) / float64(25-1) * 100.0)

	case blocks < 100:
		stats.Level = 2
		stats.Title = "Signal Scraper"
		stats.CurrentXP = blocks
		stats.NextLevelXP = 100
		stats.ProgressPercent = roundPercent(float64(blocks-25) / float64(100-25) * 100.0)

	case blocks < 500:
		stats.Level = 3
		stats.Title = "Hash Warden"
		stats.CurrentXP = blocks
		stats.NextLevelXP = 500
		stats.ProgressPercent = roundPercent(float64(blocks-100) / float64(500-100) * 100.0)

	case blocks < 2500:
		stats.Level = 4
		stats.Title = "Cipher Architect"
		stats.CurrentXP = blocks
		stats.NextLevelXP = 2500
		stats.ProgressPercent = roundPercent(float64(blocks-500) / float64(2500-500) * 100.0)

	case blocks < 10000:
		stats.Level = 5
		stats.Title = "Apex Operator"
		stats.CurrentXP = blocks
		stats.NextLevelXP = 10000
		stats.ProgressPercent = roundPercent(float64(blocks-2500) / float64(10000-2500) * 100.0)

	default:
		stats.Level = 6
		stats.Title = "Rogue Singularity"
		stats.CurrentXP = blocks
		stats.NextLevelXP = 10000
		stats.ProgressPercent = 100.0
	}

	if stats.ProgressPercent < 0.0 {
		stats.ProgressPercent = 0.0
	} else if stats.ProgressPercent > 100.0 {
		stats.ProgressPercent = 100.0
	}

	return stats
}

func roundPercent(val float64) float64 {
	return math.Round(val*100) / 100
}

