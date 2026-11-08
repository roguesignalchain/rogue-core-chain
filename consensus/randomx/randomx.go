package randomx

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"math/rand"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/rpc"
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

// RandomX implements the consensus.Engine interface for Rogue Signal using RandomX (rx/0).
type RandomX struct {
	fastMode bool
	threads  int
	treasury common.Address

	epochs *EpochManager

	lock sync.RWMutex
}

// Config defines the configuration options for RandomX consensus engine.
type Config struct {
	FastMode bool
	Threads  int
	Treasury common.Address
}

// New creates a full RandomX consensus engine.
func New(cfg Config) *RandomX {
	threads := cfg.Threads
	if threads <= 0 {
		threads = 1 // Default to 1 thread in light mode to protect low-spec machines
	}

	treasury := cfg.Treasury
	if treasury == (common.Address{}) {
		treasury = DefaultTreasuryAddress
	}

	return &RandomX{
		fastMode: cfg.FastMode,
		threads:  threads,
		treasury: treasury,
		epochs:   NewEpochManager(cfg.FastMode),
	}
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

	go func() {
		for i, header := range headers {
			err := rx.VerifyHeader(chain, header, seals[i])
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

	// Assemble block without uncles
	return types.NewBlock(header, txs, nil, receipts, nil), nil
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

// MiningInput serializes SealHash(header) and the 64-bit Nonce into a 40-byte RandomX mining blob.
func MiningInput(sealHash common.Hash, nonce uint64) []byte {
	input := make([]byte, 40)
	copy(input[:32], sealHash.Bytes())
	binary.LittleEndian.PutUint64(input[32:], nonce)
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

// Seal implements the built-in miner loop for testing and single-node block generation.
func (rx *RandomX) Seal(chain consensus.ChainHeaderReader, block *types.Block, results chan<- *types.Block, stop <-chan struct{}) error {
	header := block.Header()
	number := header.Number.Uint64()

	ctx, err := rx.epochs.GetContext(number, chain)
	if err != nil {
		return fmt.Errorf("failed to get RandomX context for mining block %d: %w", number, err)
	}

	target := DifficultyToTarget(header.Difficulty)
	sealHash := rx.SealHash(header)

	threads := rx.threads
	if threads < 1 {
		threads = 1
	}

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

			for {
				select {
				case <-stop:
					return
				case <-found:
					return
				default:
				}

				input := MiningInput(sealHash, nonce)
				rxHash, err := ctx.CalculateHash(input)
				if err != nil {
					return
				}

				hashNum := HashToBig(rxHash)
				if hashNum.Cmp(target) <= 0 {
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

// CalcDifficulty returns the difficulty of the next block.
func (rx *RandomX) CalcDifficulty(chain consensus.ChainHeaderReader, time uint64, parent *types.Header) *big.Int {
	return CalculateDifficulty(chain, time, parent)
}

// APIs returns consensus RPC APIs.
func (rx *RandomX) APIs(chain consensus.ChainHeaderReader) []rpc.API {
	return nil
}

// Close releases all RandomX caches and memory.
func (rx *RandomX) Close() error {
	rx.epochs.Close()
	return nil
}
