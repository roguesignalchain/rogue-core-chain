package randomx

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
)

const (
	// HalvingInterval is the number of blocks between reward halvings (6,300,000 blocks = ~2 years at 10s/block).
	HalvingInterval = uint64(6300000)

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

	// InitialBaseReward is 8 ROGUE in wei.
	InitialBaseReward = new(big.Int).Mul(big.NewInt(8), WeiPerROGUE)

	// MaxSupplyCap is approximately 100,000,000 ROGUE in wei (exact theoretical total: 100,800,000 ROGUE).
	MaxSupplyCap = new(big.Int).Mul(big.NewInt(100800000), WeiPerROGUE)

	// DefaultTreasuryAddress is the default protocol treasury address for Rogue Signal.
	DefaultTreasuryAddress = common.HexToAddress("0xe9aaE535B7971A9AC8C6af0e4B524D02260D3a03")
)

// CalculateBlockReward calculates the total block reward for a given block number.
//
// Rules:
//   - Era 0 (blocks 1 .. 6,300,000): 8 ROGUE (8 * 10^18 wei)
//   - Era 1 (blocks 6,300,001 .. 12,600,000): 4 ROGUE
//   - Era 2 (blocks 12,600,001 .. 18,900,000): 2 ROGUE
//   - Halves every 6,300,000 blocks.
//   - After era 32 (or if reward shifts to 0), returns 0 (Strict Cap).
func CalculateBlockReward(blockNumber uint64) *big.Int {
	if blockNumber == 0 {
		return big.NewInt(0) // Genesis block has 0 reward
	}

	era := (blockNumber - 1) / HalvingInterval
	if era >= MaxHalvingEras {
		return big.NewInt(0)
	}

	reward := new(big.Int).Rsh(InitialBaseReward, uint(era))
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

// AccumulateRewards credits the block rewards to the miner coinbase (95%) and protocol treasury (5%).
func AccumulateRewards(state *state.StateDB, header *types.Header, treasury common.Address) {
	totalReward := CalculateBlockReward(header.Number.Uint64())
	if totalReward.Sign() <= 0 {
		return
	}

	minerReward, treasuryReward := SplitReward(totalReward)

	// Credit miner (95%)
	if minerReward.Sign() > 0 {
		state.AddBalance(header.Coinbase, uint256.MustFromBig(minerReward))
	}

	// Credit treasury (5%)
	if treasuryReward.Sign() > 0 {
		targetTreasury := treasury
		if targetTreasury == (common.Address{}) {
			targetTreasury = DefaultTreasuryAddress
		}
		state.AddBalance(targetTreasury, uint256.MustFromBig(treasuryReward))
	}
}
