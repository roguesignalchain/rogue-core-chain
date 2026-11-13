package randomx

import (
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/types"
)

// ErrRemoteMiningDisabled is returned when external/remote miners attempt to request or submit PoW work.
// Rogue Signal restricts mining strictly to the internal node CPU engine to prevent external Stratum proxies and specialized rigs.
var ErrRemoteMiningDisabled = errors.New("remote mining (eth_getWork / eth_submitWork) is disabled on Rogue Signal: only internal node CPU mining is supported")

// EthAPI exposes only standard read-only/stub PoW methods on the public "eth" namespace.
// Administrative node controls such as SetFastMode are intentionally excluded from "eth"
// to prevent unauthenticated memory-allocation DoS on public RPC endpoints.
type EthAPI struct {
	rx *RandomX
}

// GetWork is disabled on Rogue Signal to prevent external Stratum bridges and mining rigs.
func (api *EthAPI) GetWork() ([4]string, error) {
	return [4]string{}, ErrRemoteMiningDisabled
}

// SubmitWork is disabled on Rogue Signal to prevent external Stratum bridges and mining rigs.
func (api *EthAPI) SubmitWork(nonce types.BlockNonce, hash, digest common.Hash) bool {
	return false
}

// SubmitHashrate is disabled on Rogue Signal as external miners are not supported.
func (api *EthAPI) SubmitHashrate(rate hexutil.Uint64, id common.Hash) bool {
	return false
}

// GetHashrate returns the current hashrate of the internal local CPU miner.
func (api *EthAPI) GetHashrate() uint64 {
	return uint64(api.rx.Hashrate())
}

// API exposes RandomX consensus diagnostic and control methods under the "randomx" namespace.
type API struct {
	rx    *RandomX
	chain consensus.ChainHeaderReader
}

// GetWork is disabled on Rogue Signal to prevent external Stratum bridges and mining rigs.
func (api *API) GetWork() ([4]string, error) {
	return [4]string{}, ErrRemoteMiningDisabled
}

// SubmitWork is disabled on Rogue Signal to prevent external Stratum bridges and mining rigs.
func (api *API) SubmitWork(nonce types.BlockNonce, hash, digest common.Hash) bool {
	return false
}

// SubmitHashrate is disabled on Rogue Signal as external miners are not supported.
func (api *API) SubmitHashrate(rate hexutil.Uint64, id common.Hash) bool {
	return false
}

// GetHashrate returns the current hashrate of the internal local CPU miner.
func (api *API) GetHashrate() uint64 {
	return uint64(api.rx.Hashrate())
}

// SetFastMode toggles between RandomX Light Mode (~256MB) and Fast Mode (~2.08GB) on the local node.
func (api *API) SetFastMode(fastMode bool) bool {
	if api.rx.IsFastMode() == fastMode {
		return true
	}
	api.rx.SetFastMode(fastMode)
	return true
}

// GetFastMode returns true if RandomX Fast Mode (~2.08GB) is enabled on the local node.
func (api *API) GetFastMode() bool {
	return api.rx.IsFastMode()
}

// EpochInfo represents the RandomX epoch and seed rotation status at the current chain head.
type EpochInfo struct {
	BlockNumber          uint64      `json:"blockNumber"`
	Epoch                uint64      `json:"epoch"`
	EpochLength          uint64      `json:"epochLength"`
	SeedLag              uint64      `json:"seedLag"`
	SeedBlockNumber      uint64      `json:"seedBlockNumber"`
	SeedHash             common.Hash `json:"seedHash"`
	NextEpochBlock       uint64      `json:"nextEpochBlock"`
	BlocksUntilNextEpoch uint64      `json:"blocksUntilNextEpoch"`
	FastMode             bool        `json:"fastMode"`
}

// VMPoolInfo represents the live telemetry of the bounded RandomX VM pool.
type VMPoolInfo struct {
	TotalVMs          int64  `json:"totalVMs"`
	ActiveVMs         int64  `json:"activeVMs"`
	Mode              string `json:"mode"`
	AllocatedMemoryMB uint64 `json:"allocatedMemoryMB"`
	LargePages        bool   `json:"largePages"`
}

// ConsensusStatus represents the real-time consensus parameters, monetary schedule, and active security defenses.
type ConsensusStatus struct {
	Algorithm           string         `json:"algorithm"`
	TargetBlockTime     uint64         `json:"targetBlockTime"`
	LWMAWindow          uint64         `json:"lwmaWindow"`
	MTPWindow           uint64         `json:"mtpWindow"`
	MaxFutureDrift      uint64         `json:"maxFutureDrift"`
	MaxExtraDataSize    uint64         `json:"maxExtraDataSize"`
	FinalityMode        string         `json:"finalityMode"`
	MESSActive          bool           `json:"messActive"`
	UnclesAllowed       bool           `json:"unclesAllowed"`
	RemoteMiningAllowed bool           `json:"remoteMiningAllowed"`
	CurrentBlock        uint64         `json:"currentBlock"`
	CurrentEra          uint64         `json:"currentEra"`
	HalvingInterval     uint64         `json:"halvingInterval"`
	BlocksUntilHalving  uint64         `json:"blocksUntilHalving"`
	BlockRewardWei      string         `json:"blockRewardWei"`
	MinerRewardWei      string         `json:"minerRewardWei"`
	TreasuryRewardWei   string         `json:"treasuryRewardWei"`
	TreasuryAddress     common.Address `json:"treasuryAddress"`
	FastMode            bool           `json:"fastMode"`
	LocalHashrate       uint64         `json:"localHashrate"`
	VMPool              VMPoolInfo     `json:"vmPool"`
	EpochInfo           EpochInfo      `json:"epochInfo"`
}

// GetEpochInfo returns the current RandomX epoch, seed block, seed hash, and transition countdown.
func (api *API) GetEpochInfo() EpochInfo {
	var headNum uint64
	if api.chain != nil && api.chain.CurrentHeader() != nil {
		headNum = api.chain.CurrentHeader().Number.Uint64()
	}

	epoch := EpochNumber(headNum)
	seedBlock := SeedBlockNumber(epoch)
	var seedHash common.Hash
	if api.chain != nil {
		if h, err := api.rx.epochs.GetSeed(epoch, api.chain); err == nil {
			seedHash = h
		} else if hdr := api.chain.GetHeaderByNumber(seedBlock); hdr != nil {
			seedHash = hdr.Hash()
		}
	}

	nextEpochBlock := (epoch + 1) * EpochLength
	blocksRemaining := nextEpochBlock - headNum

	return EpochInfo{
		BlockNumber:          headNum,
		Epoch:                epoch,
		EpochLength:          EpochLength,
		SeedLag:              SeedLag,
		SeedBlockNumber:      seedBlock,
		SeedHash:             seedHash,
		NextEpochBlock:       nextEpochBlock,
		BlocksUntilNextEpoch: blocksRemaining,
		FastMode:             api.rx.IsFastMode(),
	}
}

// GetConsensusStatus returns the comprehensive consensus and security status of the Rogue Signal node.
func (api *API) GetConsensusStatus() ConsensusStatus {
	epochInfo := api.GetEpochInfo()
	headNum := epochInfo.BlockNumber

	calcBlock := headNum
	if calcBlock == 0 {
		calcBlock = 1
	}
	era := (calcBlock - 1) / HalvingInterval
	nextHalvingBlock := (era + 1) * HalvingInterval
	var blocksUntilHalving uint64
	if nextHalvingBlock > headNum {
		blocksUntilHalving = nextHalvingBlock - headNum
	}

	totalReward := CalculateBlockReward(calcBlock)
	minerReward, treasuryReward := SplitReward(totalReward)

	totalVMs, activeVMs, allocatedMB := api.rx.epochs.VMPoolStats()
	poolMode := "light"
	if api.rx.IsFastMode() {
		poolMode = "fast"
	}

	return ConsensusStatus{
		Algorithm:           "RandomX (rx/0)",
		TargetBlockTime:     DefaultTargetBlockTime,
		LWMAWindow:          DefaultLWMAWindow,
		MTPWindow:           MTPWindow,
		MaxFutureDrift:      MaxFutureDrift,
		MaxExtraDataSize:    MaxExtraDataSize,
		FinalityMode:        "ECBP-1100 (MESS)",
		MESSActive:          true,
		UnclesAllowed:       false,
		RemoteMiningAllowed: false,
		CurrentBlock:        headNum,
		CurrentEra:          era,
		HalvingInterval:     HalvingInterval,
		BlocksUntilHalving:  blocksUntilHalving,
		BlockRewardWei:      totalReward.String(),
		MinerRewardWei:      minerReward.String(),
		TreasuryRewardWei:   treasuryReward.String(),
		TreasuryAddress:     api.rx.treasury,
		FastMode:            api.rx.IsFastMode(),
		LocalHashrate:       uint64(api.rx.Hashrate()),
		VMPool: VMPoolInfo{
			TotalVMs:          totalVMs,
			ActiveVMs:         activeVMs,
			Mode:              poolMode,
			AllocatedMemoryMB: allocatedMB,
			LargePages:        api.rx.epochs.LargePages(),
		},
		EpochInfo:           epochInfo,
	}
}
