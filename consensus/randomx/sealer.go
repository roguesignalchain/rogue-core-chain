package randomx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

const (
	staleThreshold      = 7
	remoteSealerTimeout = 1 * time.Second
)

var (
	errNoMiningWork      = errors.New("no mining work available yet")
	errInvalidSealResult = errors.New("invalid or stale proof-of-work solution")
)

type remoteSealer struct {
	rx           *RandomX
	noverify     bool
	notifyURLs   []string
	notifyCtx    context.Context
	cancelNotify context.CancelFunc
	reqWG        sync.WaitGroup

	chain        consensus.ChainHeaderReader
	works        map[common.Hash]*types.Block
	rates        map[common.Hash]hashrate
	currentBlock *types.Block
	currentWork  [4]string
	results      chan<- *types.Block

	workCh       chan *sealTask
	fetchWorkCh  chan *sealWork
	submitWorkCh chan *mineResult
	fetchRateCh  chan chan uint64
	submitRateCh chan *hashrate
	requestExit  chan struct{}
	exitCh       chan struct{}
}

type sealTask struct {
	block   *types.Block
	results chan<- *types.Block
	chain   consensus.ChainHeaderReader
}

type mineResult struct {
	nonce     types.BlockNonce
	mixDigest common.Hash
	hash      common.Hash
	errc      chan error
}

type hashrate struct {
	id   common.Hash
	ping time.Time
	rate uint64
	done chan struct{}
}

type sealWork struct {
	errc chan error
	res  chan [4]string
}

func startRemoteSealer(rx *RandomX, urls []string, noverify bool) *remoteSealer {
	ctx, cancel := context.WithCancel(context.Background())
	s := &remoteSealer{
		rx:           rx,
		noverify:     noverify,
		notifyURLs:   urls,
		notifyCtx:    ctx,
		cancelNotify: cancel,
		works:        make(map[common.Hash]*types.Block),
		rates:        make(map[common.Hash]hashrate),
		workCh:       make(chan *sealTask),
		fetchWorkCh:  make(chan *sealWork),
		submitWorkCh: make(chan *mineResult),
		fetchRateCh:  make(chan chan uint64),
		submitRateCh: make(chan *hashrate),
		requestExit:  make(chan struct{}),
		exitCh:       make(chan struct{}),
	}
	go s.loop()
	return s
}

func (s *remoteSealer) close() {
	close(s.requestExit)
	<-s.exitCh
}

func (s *remoteSealer) loop() {
	defer func() {
		log.Trace("RandomX remote sealer is exiting")
		s.cancelNotify()
		s.reqWG.Wait()
		close(s.exitCh)
	}()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case task := <-s.workCh:
			s.results = task.results
			s.chain = task.chain
			s.makeWork(task.block, task.chain)
			s.notifyWork()

		case work := <-s.fetchWorkCh:
			if s.currentBlock == nil {
				work.errc <- errNoMiningWork
			} else {
				work.res <- s.currentWork
			}

		case result := <-s.submitWorkCh:
			if s.submitWork(result.nonce, result.mixDigest, result.hash) {
				result.errc <- nil
			} else {
				result.errc <- errInvalidSealResult
			}

		case result := <-s.submitRateCh:
			s.rates[result.id] = hashrate{rate: result.rate, ping: time.Now()}
			close(result.done)

		case req := <-s.fetchRateCh:
			var total uint64
			for _, rate := range s.rates {
				total += rate.rate
			}
			req <- total

		case <-ticker.C:
			for id, rate := range s.rates {
				if time.Since(rate.ping) > 10*time.Second {
					delete(s.rates, id)
				}
			}
			if s.currentBlock != nil {
				for hash, block := range s.works {
					if block.NumberU64()+staleThreshold <= s.currentBlock.NumberU64() {
						delete(s.works, hash)
					}
				}
			}

		case <-s.requestExit:
			return
		}
	}
}

func (s *remoteSealer) makeWork(block *types.Block, chain consensus.ChainHeaderReader) {
	hash := s.rx.SealHash(block.Header())
	epoch := EpochNumber(block.NumberU64())

	var seedHash common.Hash
	if chain != nil {
		if seed, err := s.rx.epochs.GetSeed(epoch, chain); err == nil {
			seedHash = seed
		}
	}
	target := DifficultyToTarget(block.Difficulty())

	s.currentWork[0] = hash.Hex()
	s.currentWork[1] = seedHash.Hex()
	s.currentWork[2] = common.BytesToHash(target.Bytes()).Hex()
	s.currentWork[3] = hexutil.EncodeBig(block.Number())

	s.currentBlock = block
	s.works[hash] = block
}

func (s *remoteSealer) notifyWork() {
	if len(s.notifyURLs) == 0 {
		return
	}
	work := s.currentWork
	blob, _ := json.Marshal(work)
	s.reqWG.Add(len(s.notifyURLs))
	for _, url := range s.notifyURLs {
		go s.sendNotification(s.notifyCtx, url, blob, work)
	}
}

func (s *remoteSealer) sendNotification(ctx context.Context, url string, jsonBlob []byte, work [4]string) {
	defer s.reqWG.Done()

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(jsonBlob))
	if err != nil {
		log.Warn("Can't create remote miner notification", "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, remoteSealerTimeout)
	defer cancel()
	req = req.WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Warn("Failed to notify remote miner", "err", err)
	} else {
		resp.Body.Close()
	}
}

func (s *remoteSealer) submitWork(nonce types.BlockNonce, mixDigest common.Hash, sealhash common.Hash) bool {
	if s.currentBlock == nil {
		log.Error("Pending work without block", "sealhash", sealhash)
		return false
	}
	block := s.works[sealhash]
	if block == nil {
		log.Warn("Work submitted but none pending", "sealhash", sealhash, "curnumber", s.currentBlock.NumberU64())
		return false
	}

	header := block.Header()
	header.Nonce = nonce
	header.MixDigest = mixDigest

	if !s.noverify {
		if err := s.rx.VerifySeal(s.chain, header); err != nil {
			log.Warn("Invalid RandomX proof-of-work submitted", "sealhash", sealhash, "err", err)
			return false
		}
	}

	if s.results == nil {
		log.Warn("RandomX result channel is empty, submitted mining result is rejected")
		return false
	}

	solution := block.WithSeal(header)
	if solution.NumberU64()+staleThreshold > s.currentBlock.NumberU64() {
		select {
		case s.results <- solution:
			log.Info("Successfully sealed block via remote miner",
				"number", solution.NumberU64(),
				"nonce", header.Nonce.Uint64(),
				"hash", solution.Hash().Hex(),
			)
			return true
		default:
			log.Warn("Sealing result is not read by miner", "mode", "remote", "sealhash", sealhash)
			return false
		}
	}
	log.Warn("Work submitted is too old", "number", solution.NumberU64(), "sealhash", sealhash)
	return false
}
