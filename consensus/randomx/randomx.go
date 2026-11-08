package randomx

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/metrics"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/ethereum/go-ethereum/trie"
	"golang.org/x/crypto/sha3"
)

var (
	// ErrInvalidPoW is returned when the RandomX proof of work does not meet the required target.
	ErrInvalidPoW = errors.New("invalid RandomX proof of work")

	// ErrInvalidMixDigest is returned when header.MixDigest does not match the computed RandomX hash.
	ErrInvalidMixDigest = errors.New("invalid RandomX mix digest")

	// ErrUnclesNotAllowed is returned when a block contains uncle headers (disabled on Rogue Signal).
	ErrUnclesNotAllowed = errors.New("uncles not allowed on Rogue Signal")
)

type hashrateTracker struct {
	mu          sync.Mutex
	lastTick    time.Time
	lastHashes  int64
	currentRate float64
	totalHashes atomic.Int64
}

func (ht *hashrateTracker) Mark(attempts int64) {
	if attempts <= 0 {
		return
	}
	ht.totalHashes.Add(attempts)
}

func (ht *hashrateTracker) Rate() float64 {
	ht.mu.Lock()
	defer ht.mu.Unlock()

	now := time.Now()
	if ht.lastTick.IsZero() {
		ht.lastTick = now
		ht.lastHashes = ht.totalHashes.Load()
		return ht.currentRate
	}

	elapsed := now.Sub(ht.lastTick).Seconds()
	if elapsed >= 1.0 {
		currHashes := ht.totalHashes.Load()
		delta := currHashes - ht.lastHashes
		if delta > 0 {
			instant := float64(delta) / elapsed
			if ht.currentRate == 0 {
				ht.currentRate = instant
			} else {
				ht.currentRate = 0.7*instant + 0.3*ht.currentRate
			}
		} else if elapsed > 4.0 {
			ht.currentRate = 0
		}
		ht.lastHashes = currHashes
		ht.lastTick = now
	} else if now.Sub(ht.lastTick).Seconds() > 4.0 {
		ht.currentRate = 0
	}

	return ht.currentRate
}

// RandomX implements the consensus.Engine and consensus.PoW interfaces for Rogue Signal using RandomX (rx/0).
type RandomX struct {
	fastMode bool
	threads  int
	treasury common.Address

	epochs *EpochManager
	remote *remoteSealer

	tracker  hashrateTracker
	hashrate metrics.Meter

	lock sync.RWMutex
}

// Config defines the configuration options for RandomX consensus engine.
type Config struct {
	FastMode   bool
	Threads    int
	Treasury   common.Address
	NotifyURLs []string
	NoVerify   bool
}

// New creates a full RandomX consensus engine.
func New(cfg Config) *RandomX {
	treasury := cfg.Treasury
	if treasury == (common.Address{}) {
		treasury = DefaultTreasuryAddress
	}

	rx := &RandomX{
		fastMode: cfg.FastMode,
		threads:  cfg.Threads,
		treasury: treasury,
		epochs:   NewEpochManager(cfg.FastMode),
		hashrate: metrics.NewMeter(),
	}
	rx.remote = startRemoteSealer(rx, cfg.NotifyURLs, cfg.NoVerify)
	return rx
}

// SetThreads updates the number of mining threads dynamically (-1 to disable local mining).
func (rx *RandomX) SetThreads(threads int) {
	rx.lock.Lock()
	defer rx.lock.Unlock()
	rx.threads = threads
}

// headerChainReader wraps a ChainHeaderReader with an in-memory batch of headers
// so ancestors within the batch can be resolved before they are committed to the chain database.
type headerChainReader struct {
	consensus.ChainHeaderReader
	byHash   map[common.Hash]*types.Header
	byNumber map[uint64]*types.Header
}

func (r *headerChainReader) GetHeader(hash common.Hash, number uint64) *types.Header {
	if h, ok := r.byHash[hash]; ok {
		return h
	}
	return r.ChainHeaderReader.GetHeader(hash, number)
}

func (r *headerChainReader) GetHeaderByHash(hash common.Hash) *types.Header {
	if h, ok := r.byHash[hash]; ok {
		return h
	}
	return r.ChainHeaderReader.GetHeaderByHash(hash)
}

func (r *headerChainReader) GetHeaderByNumber(number uint64) *types.Header {
	if h, ok := r.byNumber[number]; ok {
		return h
	}
	return r.ChainHeaderReader.GetHeaderByNumber(number)
}

// Author implements consensus.Engine, returning the block coinbase.
func (rx *RandomX) Author(header *types.Header) (common.Address, error) {
	return header.Coinbase, nil
}

// VerifyHeader checks whether a header conforms to the Rogue Signal consensus rules.
func (rx *RandomX) VerifyHeader(chain consensus.ChainHeaderReader, header *types.Header, seal bool) error {
	// Genesis block is always valid
	number := header.Number.Uint64()
	if number == 0 {
		return nil
	}

	parent := chain.GetHeader(header.ParentHash, number-1)
	if parent == nil {
		return consensus.ErrUnknownAncestor
	}

	return rx.verifyHeader(chain, header, parent, seal)
}

// verifyHeader performs full header validation given an already resolved parent header.
func (rx *RandomX) verifyHeader(chain consensus.ChainHeaderReader, header, parent *types.Header, seal bool) error {
	// 1. Verify strict timestamp rules (MTP-11 + max 15s future drift)
	now := uint64(time.Now().Unix())
	if err := VerifyHeaderTime(chain, header, parent, now); err != nil {
		return err
	}

	// 2. Verify difficulty adjustment via LWMA-1
	expectedDifficulty := CalculateDifficulty(chain, header.Time, parent)
	if header.Difficulty.Cmp(expectedDifficulty) != 0 {
		return fmt.Errorf("invalid difficulty: have %v, want %v", header.Difficulty, expectedDifficulty)
	}

	// 3. Verify gas limit
	if header.GasUsed > header.GasLimit {
		return fmt.Errorf("gas used %d exceeds gas limit %d", header.GasUsed, header.GasLimit)
	}

	// 4. Verify seal if requested
	if seal {
		if err := rx.VerifySeal(chain, header); err != nil {
			return err
		}
	}

	return nil
}

// VerifyHeaders verifies a batch of headers concurrently.
func (rx *RandomX) VerifyHeaders(chain consensus.ChainHeaderReader, headers []*types.Header, seals []bool) (chan<- struct{}, <-chan error) {
	abort := make(chan struct{})
	results := make(chan error, len(headers))

	if len(headers) == 0 {
		return abort, results
	}

	byHash := make(map[common.Hash]*types.Header, len(headers))
	byNumber := make(map[uint64]*types.Header, len(headers))
	for _, h := range headers {
		byHash[h.Hash()] = h
		byNumber[h.Number.Uint64()] = h
	}
	batchReader := &headerChainReader{
		ChainHeaderReader: chain,
		byHash:            byHash,
		byNumber:          byNumber,
	}

	go func() {
		for i, header := range headers {
			number := header.Number.Uint64()
			var parent *types.Header
			if number == 0 {
				select {
				case <-abort:
					return
				case results <- nil:
				}
				continue
			}

			if i > 0 && headers[i-1].Hash() == header.ParentHash {
				parent = headers[i-1]
			} else {
				parent = batchReader.GetHeader(header.ParentHash, number-1)
			}

			var err error
			if parent == nil {
				err = consensus.ErrUnknownAncestor
			} else {
				err = rx.verifyHeader(batchReader, header, parent, seals[i])
			}

			select {
			case <-abort:
				return
			case results <- err:
			}
		}
	}()

	return abort, results
}

// VerifyUncles enforces that Rogue Signal strictly forbids uncle blocks (Bitcoin model).
func (rx *RandomX) VerifyUncles(chain consensus.ChainReader, block *types.Block) error {
	if len(block.Uncles()) > 0 {
		return ErrUnclesNotAllowed
	}
	return nil
}

// Prepare initializes consensus fields on the header before execution.
func (rx *RandomX) Prepare(chain consensus.ChainHeaderReader, header *types.Header) error {
	parent := chain.GetHeader(header.ParentHash, header.Number.Uint64()-1)
	if parent == nil {
		return consensus.ErrUnknownAncestor
	}
	header.Difficulty = CalculateDifficulty(chain, header.Time, parent)
	header.UncleHash = types.EmptyUncleHash

	// Trigger background precomputation of next epoch cache if inside seed lag window
	rx.epochs.PrecomputeNextEpoch(header.Number.Uint64(), chain)
	return nil
}

// Finalize credits mining rewards to the miner coinbase (95%) and treasury (5%).
func (rx *RandomX) Finalize(chain consensus.ChainHeaderReader, header *types.Header, state *state.StateDB, txs []*types.Transaction,
	uncles []*types.Header, withdrawals []*types.Withdrawal) {
	AccumulateRewards(state, header, rx.treasury)
}

// FinalizeAndAssemble credits rewards and constructs the final block.
func (rx *RandomX) FinalizeAndAssemble(chain consensus.ChainHeaderReader, header *types.Header, state *state.StateDB, txs []*types.Transaction,
	uncles []*types.Header, receipts []*types.Receipt, withdrawals []*types.Withdrawal) (*types.Block, error) {

	if len(uncles) > 0 {
		return nil, ErrUnclesNotAllowed
	}

	rx.Finalize(chain, header, state, txs, uncles, withdrawals)
	header.Root = state.IntermediateRoot(true)

	// Assemble block without uncles using StackTrie hasher for tx and receipt trees
	return types.NewBlock(header, txs, nil, receipts, trie.NewStackTrie(nil)), nil
}

// SealHash calculates the pre-seal hash of the block header using Ethereum RLP encoding.
func (rx *RandomX) SealHash(header *types.Header) common.Hash {
	hasher := sha3.NewLegacyKeccak256()

	enc := []interface{}{
		header.ParentHash,
		header.UncleHash,
		header.Coinbase,
		header.Root,
		header.TxHash,
		header.ReceiptHash,
		header.Bloom,
		header.Difficulty,
		header.Number,
		header.GasLimit,
		header.GasUsed,
		header.Time,
		header.Extra,
	}
	if header.BaseFee != nil {
		enc = append(enc, header.BaseFee)
	}

	rlp.Encode(hasher, enc)
	var hash common.Hash
	hasher.Sum(hash[:0])
	return hash
}

// MiningBlobSize defines the canonical 76-byte RandomX mining blob (Monero/rx0 standard).
const MiningBlobSize = 76

// MiningInput serializes SealHash(header) and the 64-bit Nonce into a standard 76-byte RandomX mining blob.
// Offset 0..31: SealHash (32 bytes)
// Offset 32..38: Extra nonce / reserved (7 bytes)
// Offset 39..46: Nonce uint64 (8 bytes, little-endian; bytes 39..42 match standard XMRig 32-bit nonce)
// Offset 47..75: Padding (29 bytes)
func MiningInput(sealHash common.Hash, nonce uint64) []byte {
	input := make([]byte, MiningBlobSize)
	copy(input[:32], sealHash.Bytes())
	binary.LittleEndian.PutUint64(input[39:], nonce)
	return input
}

// VerifySeal checks if the header contains a valid RandomX proof of work.
func (rx *RandomX) VerifySeal(chain consensus.ChainHeaderReader, header *types.Header) error {
	number := header.Number.Uint64()
	if number == 0 {
		return nil // Genesis
	}

	ctx, err := rx.epochs.GetContext(number, chain)
	if err != nil {
		return fmt.Errorf("failed to get RandomX context for block %d: %w", number, err)
	}

	sealHash := rx.SealHash(header)
	input := MiningInput(sealHash, header.Nonce.Uint64())

	rxHash, err := ctx.CalculateHash(input)
	if err != nil {
		return fmt.Errorf("RandomX hash calculation failed: %w", err)
	}

	// Verify MixDigest matches the RandomX hash output
	if !bytes.Equal(header.MixDigest.Bytes(), rxHash[:]) {
		return ErrInvalidMixDigest
	}

	// Verify PoW difficulty target: HashToBig(rxHash) <= Target
	target := DifficultyToTarget(header.Difficulty)
	hashNum := HashToBig(rxHash)
	if hashNum.Cmp(target) > 0 {
		return ErrInvalidPoW
	}

	return nil
}

// Seal implements the consensus.Engine interface, coordinating local CPU miner threads and remote Stratum miners.
func (rx *RandomX) Seal(chain consensus.ChainHeaderReader, block *types.Block, results chan<- *types.Block, stop <-chan struct{}) error {
	header := block.Header()
	number := header.Number.Uint64()

	// Push new work to remote sealer for Stratum / RPC external miners
	if rx.remote != nil {
		rx.remote.workCh <- &sealTask{block: block, results: results, chain: chain}
	}

	rx.lock.RLock()
	threads := rx.threads
	rx.lock.RUnlock()

	// If local CPU mining is disabled (e.g. delegated entirely to Stratum / XMRig), wait for completion
	if threads <= 0 {
		<-stop
		return nil
	}

	ctx, err := rx.epochs.GetContext(number, chain)
	if err != nil {
		return fmt.Errorf("failed to get RandomX context for mining block %d: %w", number, err)
	}

	target := DifficultyToTarget(header.Difficulty)
	sealHash := rx.SealHash(header)

	var (
		foundNonce uint64
		foundHash  [32]byte
		found      = make(chan struct{})
		abortOnce  sync.Once
	)

	var wg sync.WaitGroup
	for threadID := 0; threadID < threads; threadID++ {
		wg.Add(1)
		go func(tid int) {
			defer wg.Done()

			// Each thread starts searching from an independent randomized nonce offset
			nonce := rand.Uint64() + uint64(tid)*0x100000000
			var attempts int64

			for {
				select {
				case <-stop:
					rx.markAttempts(attempts)
					return
				case <-found:
					rx.markAttempts(attempts)
					return
				default:
				}

				attempts++
				if (attempts % 16) == 0 {
					rx.markAttempts(attempts)
					attempts = 0
				}

				input := MiningInput(sealHash, nonce)
				rxHash, err := ctx.CalculateHash(input)
				if err != nil {
					rx.markAttempts(attempts)
					return
				}

				hashNum := HashToBig(rxHash)
				if hashNum.Cmp(target) <= 0 {
					rx.markAttempts(attempts)
					abortOnce.Do(func() {
						foundNonce = nonce
						foundHash = rxHash
						close(found)
					})
					return
				}

				nonce++
			}
		}(threadID)
	}

	select {
	case <-stop:
		wg.Wait()
		return nil
	case <-found:
		wg.Wait()
		// Assemble winning header
		header.Nonce = types.EncodeNonce(foundNonce)
		header.MixDigest = common.BytesToHash(foundHash[:])

		log.Info("Successfully sealed block with RandomX",
			"number", header.Number,
			"difficulty", header.Difficulty,
			"nonce", foundNonce,
			"hash", header.MixDigest.Hex(),
		)

		select {
		case results <- block.WithSeal(header):
		case <-stop:
		}
		return nil
	}
}

func (rx *RandomX) markAttempts(attempts int64) {
	if attempts <= 0 {
		return
	}
	rx.tracker.Mark(attempts)
	rx.hashrate.Mark(attempts)
}

// CalcDifficulty returns the difficulty of the next block.
func (rx *RandomX) CalcDifficulty(chain consensus.ChainHeaderReader, time uint64, parent *types.Header) *big.Int {
	return CalculateDifficulty(chain, time, parent)
}

// Hashrate implements consensus.PoW, returning the current mining hashrate of local and remote miners.
func (rx *RandomX) Hashrate() float64 {
	localRate := rx.tracker.Rate()
	if localRate == 0 {
		ms := rx.hashrate.Snapshot()
		localRate = ms.Rate1()
		if localRate == 0 && ms.Count() > 0 {
			localRate = ms.RateMean()
		}
	}

	if rx.remote == nil {
		return localRate
	}

	var res = make(chan uint64, 1)
	select {
	case rx.remote.fetchRateCh <- res:
		return localRate + float64(<-res)
	case <-rx.remote.exitCh:
		return localRate
	default:
		return localRate
	}
}

// APIs returns consensus RPC APIs for eth and randomx namespaces.
func (rx *RandomX) APIs(chain consensus.ChainHeaderReader) []rpc.API {
	return []rpc.API{
		{
			Namespace: "eth",
			Service:   &API{rx: rx, chain: chain},
		},
		{
			Namespace: "randomx",
			Service:   &API{rx: rx, chain: chain},
		},
	}
}

// Close releases all RandomX caches, remote sealer, and memory.
func (rx *RandomX) Close() error {
	if rx.remote != nil {
		rx.remote.close()
	}
	rx.epochs.Close()
	return nil
}
