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

// API exposes RandomX consensus methods for the RPC interface (eth and randomx namespaces).
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
	api.rx.SetFastMode(fastMode)
	return true
}

// GetFastMode returns true if RandomX Fast Mode (~2.08GB) is enabled on the local node.
func (api *API) GetFastMode() bool {
	return api.rx.IsFastMode()
}
