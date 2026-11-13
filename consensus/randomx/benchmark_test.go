package randomx

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func buildBenchmarkChain(count int) (*FakeChainReader, []*types.Header) {
	chain := NewFakeChainReader()
	genesis := &types.Header{
		Number:     big.NewInt(0),
		Time:       1700000000,
		Difficulty: big.NewInt(0x10000),
		GasLimit:   50000000,
		UncleHash:  types.EmptyUncleHash,
	}
	chain.AddHeader(genesis)

	headers := make([]*types.Header, 0, count)
	parent := genesis
	for i := 1; i <= count; i++ {
		h := &types.Header{
			ParentHash: parent.Hash(),
			Number:     big.NewInt(int64(i)),
			Time:       parent.Time + DefaultTargetBlockTime,
			Difficulty: big.NewInt(0x10000 + int64(i)*10),
			GasLimit:   50000000,
			UncleHash:  types.EmptyUncleHash,
		}
		chain.AddHeader(h)
		headers = append(headers, h)
		parent = h
	}
	return chain, headers
}

func BenchmarkCalculateDifficultyAndMTP11(b *testing.B) {
	chain, headers := buildBenchmarkChain(256)
	tip := headers[len(headers)-1]
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := CalculateDifficultyAndMTP11(chain, tip); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkVerifyHeadersPhase1_Batch256(b *testing.B) {
	chain, headers := buildBenchmarkChain(320)
	// Build a valid batch of 256 headers starting at block 61 with exact LWMA-1 difficulties.
	batch := make([]*types.Header, 0, 256)
	parent := headers[59]
	for i := 0; i < 256; i++ {
		diff, _, err := CalculateDifficultyAndMTP11(chain, parent)
		if err != nil {
			b.Fatal(err)
		}
		h := &types.Header{
			ParentHash: parent.Hash(),
			Number:     big.NewInt(parent.Number.Int64() + 1),
			Time:       parent.Time + DefaultTargetBlockTime,
			Difficulty: diff,
			GasLimit:   50000000,
			UncleHash:  types.EmptyUncleHash,
		}
		chain.AddHeader(h)
		batch = append(batch, h)
		parent = h
	}

	rx := New(Config{FastMode: false, Threads: -1})
	rx.nowFunc = func() uint64 { return parent.Time + 10 }
	defer rx.Close()

	seals := make([]bool, len(batch)) // Phase 1 only (seal=false)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, results := rx.VerifyHeaders(chain, batch, seals)
		for range batch {
			if err := <-results; err != nil {
				b.Fatalf("VerifyHeaders failed: %v", err)
			}
		}
	}
}

func BenchmarkVerifyHeadersPhase1_UncachedBaseline(b *testing.B) {
	chain, headers := buildBenchmarkChain(320)
	batch := make([]*types.Header, 0, 256)
	parent := headers[59]
	for i := 0; i < 256; i++ {
		diff, _, err := CalculateDifficultyAndMTP11(chain, parent)
		if err != nil {
			b.Fatal(err)
		}
		h := &types.Header{
			ParentHash: parent.Hash(),
			Number:     big.NewInt(parent.Number.Int64() + 1),
			Time:       parent.Time + DefaultTargetBlockTime,
			Difficulty: diff,
			GasLimit:   50000000,
			UncleHash:  types.EmptyUncleHash,
		}
		chain.AddHeader(h)
		batch = append(batch, h)
		parent = h
	}

	rx := New(Config{FastMode: false, Threads: -1})
	rx.nowFunc = func() uint64 { return parent.Time + 10 }
	defer rx.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		prev := headers[59]
		for _, h := range batch {
			if err := rx.verifyHeaderWithCache(chain, h, prev, false, nil); err != nil {
				b.Fatal(err)
			}
			prev = h
		}
	}
}

func BenchmarkCalculateHashWithVM_Light(b *testing.B) {
	seed := common.HexToHash("0xde4c5e747fbb072d39db2ece5a245e46b6164045e166301dfdba78f86cfbe4dd")
	ctx, err := NewRxContext(seed.Bytes(), false)
	if err != nil {
		b.Fatal(err)
	}
	defer ctx.Close()

	vm, err := ctx.AcquireVM()
	if err != nil {
		b.Fatal(err)
	}
	defer ctx.ReleaseVM(vm)

	input := MiningInput(seed, 0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ctx.CalculateHashWithVM(vm, input); err != nil {
			b.Fatal(err)
		}
	}
}
