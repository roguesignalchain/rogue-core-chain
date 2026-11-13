package randomx

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

const (
	// HalvingInterval is the number of blocks between reward halvings (2,000,000 blocks = ~1.90 years at 30s/block).
	HalvingInterval = uint64(2000000)

	// MaxHalvingEras is the maximum number of halving eras before block rewards become 0.
	MaxHalvingEras = uint64(32)

	// TreasuryFeePercent is the percentage of block reward allocated to the protocol treasury (5%).
	TreasuryFeePercent = int64(5)

	// MinerRewardPercent is the percentage of block reward allocated to the miner (95%).
	MinerRewardPercent = int64(95)

	// TotalPercent is 100%.
	TotalPercent = int64(100)
)

var (
	// WeiPerROGUE is 10^18 wei (1 ROGUE).
	WeiPerROGUE = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

	// InitialBaseReward is 25 ROGUE in wei (23.75 ROGUE miner + 1.25 ROGUE treasury at 30s/block).
	InitialBaseReward = new(big.Int).Mul(big.NewInt(25), WeiPerROGUE)

	// MaxSupplyCap is 100,000,000 ROGUE in wei (25 * 2,000,000 * 2 = 100,000,000 ROGUE exact geometric limit).
	MaxSupplyCap = new(big.Int).Mul(big.NewInt(100000000), WeiPerROGUE)

	// EraRemainderWei is the exact fractional remainder (727,595,761,750,000 wei) added to the final
	// block of each halving era so that the cumulative emission across all 32 eras equals exactly
	// 100,000,000.000000000000000000 ROGUE (zero truncation loss).
	// Total remainder across 32 eras: 32 * 727,595,761,750,000 = 23,283,064,376,000,000 wei = 0.023283064376 ROGUE.
	EraRemainderWei = big.NewInt(727595761750000)

	// DefaultTreasuryAddress is the default protocol treasury address for Rogue Signal.
	DefaultTreasuryAddress = common.HexToAddress("0xe9aaE535B7971A9AC8C6af0e4B524D02260D3a03")
)

// CalculateBlockReward calculates the total block reward for a given block number.
//
// Rules:
//   - Era 0 (blocks 1 .. 2,000,000): 25 ROGUE (25 * 10^18 wei)
//   - Era 1 (blocks 2,000,001 .. 4,000,000): 12.5 ROGUE
//   - Era 2 (blocks 4,000,001 .. 6,000,000): 6.25 ROGUE
//   - Halves every 2,000,000 blocks (~1.90 years at 30s/block).
//   - In the final block of each era (blockNumber % 2,000,000 == 0), EraRemainderWei is added
//     to compensate for integer bit-shift truncation and ensure exact 100,000,000 ROGUE hard cap.
//   - After era 32 (blockNumber > 64,000,000), returns 0 (Strict 100M Cap reached).
func CalculateBlockReward(blockNumber uint64) *big.Int {
	if blockNumber == 0 {
		return big.NewInt(0) // Genesis block has 0 reward
	}

	era := (blockNumber - 1) / HalvingInterval
	if era >= MaxHalvingEras {
		return big.NewInt(0)
	}

	reward := new(big.Int).Rsh(InitialBaseReward, uint(era))
	// Compensate bit-shift truncation in the final block of each halving era
	if blockNumber%HalvingInterval == 0 {
		reward.Add(reward, EraRemainderWei)
	}
	return reward
}

// SplitReward divides the total block reward into miner reward (95%) and treasury reward (5%).
// Total = MinerReward + TreasuryReward (preserving exact total without rounding leak).
func SplitReward(totalReward *big.Int) (minerReward *big.Int, treasuryReward *big.Int) {
	if totalReward == nil || totalReward.Sign() <= 0 {
		return big.NewInt(0), big.NewInt(0)
	}

	// treasuryReward = (totalReward * 5) / 100
	treasuryReward = new(big.Int).Mul(totalReward, big.NewInt(TreasuryFeePercent))
	treasuryReward.Div(treasuryReward, big.NewInt(TotalPercent))

	// minerReward = totalReward - treasuryReward (95%)
	minerReward = new(big.Int).Sub(totalReward, treasuryReward)
	return minerReward, treasuryReward
}

// AccumulateRewards credits the block rewards to the miner coinbase (95%) and protocol treasury (5%),
// and updates the protocol-level gamification miner block counter in StateDB.
// If header.Coinbase is unset (0x0), the miner reward and block count route to targetTreasury.
func AccumulateRewards(state *state.StateDB, header *types.Header, treasury common.Address) {
	targetTreasury := treasury
	if targetTreasury == (common.Address{}) {
		targetTreasury = DefaultTreasuryAddress
	}

	minerRecipient := header.Coinbase
	if minerRecipient == (common.Address{}) {
		minerRecipient = targetTreasury
	}

	// Record protocol-level gamification block count for the miner (active from block 1 onwards).
	if header.Number != nil && header.Number.Uint64() >= 1 {
		IncrementMinerBlockCount(state, minerRecipient)
	}

	totalReward := CalculateBlockReward(header.Number.Uint64())
	if totalReward.Sign() <= 0 {
		return
	}

	minerReward, treasuryReward := SplitReward(totalReward)

	// Credit miner (95%)
	if minerReward.Sign() > 0 {
		state.AddBalance(minerRecipient, uint256.MustFromBig(minerReward))
	}

	// Credit treasury (5%)
	if treasuryReward.Sign() > 0 {
		state.AddBalance(targetTreasury, uint256.MustFromBig(treasuryReward))
	}
}
