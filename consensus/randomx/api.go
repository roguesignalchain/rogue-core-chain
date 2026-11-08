package randomx

import (
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/types"
)

var errRandomXStopped = errors.New("randomx engine stopped")

// API exposes RandomX consensus methods for the RPC interface (eth and randomx namespaces).
type API struct {
	rx    *RandomX
	chain consensus.ChainHeaderReader
}

// GetWork returns a work package for external/remote miners.
//
// The work package consists of 4 strings:
//
//	result[0] - 32-byte hex SealHash (current block header PoW hash)
//	result[1] - 32-byte hex SeedHash (RandomX epoch seed hash)
//	result[2] - 32-byte hex Target boundary condition (2^256 / difficulty)
//	result[3] - Hex encoded block height
func (api *API) GetWork() ([4]string, error) {
	if api.rx.remote == nil {
		return [4]string{}, errors.New("remote mining not enabled")
	}

	workCh := make(chan [4]string, 1)
	errc := make(chan error, 1)

	select {
	case api.rx.remote.fetchWorkCh <- &sealWork{errc: errc, res: workCh}:
	case <-api.rx.remote.exitCh:
		return [4]string{}, errRandomXStopped
	}

	select {
	case work := <-workCh:
		return work, nil
	case err := <-errc:
		return [4]string{}, err
	}
}

// SubmitWork can be used by external miners to submit their RandomX PoW solution.
// Nonce is the 8-byte nonce, hash is the SealHash, and digest is the RandomX hash output.
func (api *API) SubmitWork(nonce types.BlockNonce, hash, digest common.Hash) bool {
	if api.rx.remote == nil {
		return false
	}

	errc := make(chan error, 1)
	select {
	case api.rx.remote.submitWorkCh <- &mineResult{
		nonce:     nonce,
		mixDigest: digest,
		hash:      hash,
		errc:      errc,
	}:
	case <-api.rx.remote.exitCh:
		return false
	}

	err := <-errc
	return err == nil
}

// SubmitHashrate records hashrate reported by remote miners.
func (api *API) SubmitHashrate(rate hexutil.Uint64, id common.Hash) bool {
	if api.rx.remote == nil {
		return false
	}

	done := make(chan struct{}, 1)
	select {
	case api.rx.remote.submitRateCh <- &hashrate{done: done, rate: uint64(rate), id: id}:
	case <-api.rx.remote.exitCh:
		return false
	}

	<-done
	return true
}

// GetHashrate returns total combined hashrate of local and remote miners.
func (api *API) GetHashrate() uint64 {
	if api.rx.remote == nil {
		return 0
	}

	res := make(chan uint64, 1)
	select {
	case api.rx.remote.fetchRateCh <- res:
		return <-res
	case <-api.rx.remote.exitCh:
		return 0
	}
}
