package randomx

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestOfficialRandomXVectorsLightMode(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		input    string
		expected string
	}{
		{
			name:     "Vector A (Light Mode)",
			key:      "test key 000",
			input:    "This is a test",
			expected: "639183aae1bf4c9a35884cb46b09cad9175f04efd7684e7262a0ac1c2f0b4e3f",
		},
		{
			name:     "Vector B (Light Mode)",
			key:      "test key 000",
			input:    "Lorem ipsum dolor sit amet",
			expected: "300a0adb47603dedb42228ccb2b211104f4da45af709cd7547cd049e9489c969",
		},
		{
			name:     "Vector C (Light Mode)",
			key:      "test key 000",
			input:    "sed do eiusmod tempor incididunt ut labore et dolore magna aliqua",
			expected: "c36d4ed4191e617309867ed66a443be4075014e2b061bcdaf9ce7b721d2b77a8",
		},
		{
			name:     "Vector D (Light Mode - New Key)",
			key:      "test key 001",
			input:    "sed do eiusmod tempor incididunt ut labore et dolore magna aliqua",
			expected: "e9ff4503201c0c2cca26d285c93ae883f9b1d30c9eb240b820756f2d5a7905fc",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, err := NewRxContext([]byte(tc.key), false) // false = Light Mode
			if err != nil {
				t.Fatalf("Failed to create RxContext: %v", err)
			}
			defer ctx.Close()

			hash, err := ctx.CalculateHash([]byte(tc.input))
			if err != nil {
				t.Fatalf("CalculateHash failed: %v", err)
			}

			actualHex := hex.EncodeToString(hash[:])
			if actualHex != tc.expected {
				t.Errorf("Hash mismatch:\nExpected: %s\nActual:   %s", tc.expected, actualHex)
			}
		})
	}
}

func TestConcurrentHashing(t *testing.T) {
	ctx, err := NewRxContext([]byte("test key 000"), false)
	if err != nil {
		t.Fatalf("Failed to create RxContext: %v", err)
	}
	defer ctx.Close()

	expectedHex := "639183aae1bf4c9a35884cb46b09cad9175f04efd7684e7262a0ac1c2f0b4e3f"
	input := []byte("This is a test")

	const goroutines = 4
	errChan := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			hash, err := ctx.CalculateHash(input)
			if err != nil {
				errChan <- err
				return
			}
			if hex.EncodeToString(hash[:]) != expectedHex {
				errChan <- err
				return
			}
			errChan <- nil
		}()
	}

	for i := 0; i < goroutines; i++ {
		if err := <-errChan; err != nil {
			t.Fatalf("Concurrent hashing failed: %v", err)
		}
	}
}

func TestConcurrentHashingAndCloseRace(t *testing.T) {
	ctx, err := NewRxContext([]byte("test key race 001"), false)
	if err != nil {
		t.Fatalf("Failed to create RxContext: %v", err)
	}

	const workers = 4
	done := make(chan struct{})
	for i := 0; i < workers; i++ {
		go func(id int) {
			for j := 0; j < 8; j++ {
				_, _ = ctx.CalculateHash([]byte("concurrent hash input under close"))
			}
			done <- struct{}{}
		}(i)
	}

	// Trigger Close concurrently while hashing goroutines are in flight
	ctx.Close()

	for i := 0; i < workers; i++ {
		<-done
	}
}

func TestHugePagesTryWithFallback(t *testing.T) {
	flags := GetRecommendedFlags(false)
	if (flags & 1) == 0 { // RANDOMX_FLAG_LARGE_PAGES == 1
		t.Fatalf("expected GetRecommendedFlags to request RANDOMX_FLAG_LARGE_PAGES, got %v", flags)
	}

	ctx, err := NewRxContext([]byte("test key 000"), false)
	if err != nil {
		t.Fatalf("NewRxContext should succeed via HugePages or automatic fallback, got: %v", err)
	}
	defer ctx.Close()

	// Verify deterministic vector hash regardless of whether HugePages were granted by the OS.
	hash, err := ctx.CalculateHash([]byte("This is a test"))
	if err != nil {
		t.Fatalf("CalculateHash failed: %v", err)
	}
	const expectedHex = "639183aae1bf4c9a35884cb46b09cad9175f04efd7684e7262a0ac1c2f0b4e3f"
	if hex.EncodeToString(hash[:]) != expectedHex {
		t.Fatalf("hash mismatch: got %s, want %s", hex.EncodeToString(hash[:]), expectedHex)
	}
	_ = ctx.LargePages()
}

func reverseToTargetBE(h [32]byte) (targetBE [32]byte) {
	for i := 0; i < 32; i++ {
		targetBE[i] = h[31-i]
	}
	return targetBE
}

func TestMineBatchWithVMParity(t *testing.T) {
	ctx, err := NewRxContext([]byte("batch-mine-test-seed"), false)
	if err != nil {
		t.Fatalf("NewRxContext failed: %v", err)
	}
	defer ctx.Close()

	if !ctx.IncRef() {
		t.Fatal("IncRef failed")
	}
	defer ctx.DecRef()

	vm, err := ctx.AcquireVM()
	if err != nil {
		t.Fatalf("AcquireVM failed: %v", err)
	}
	defer ctx.ReleaseVM(vm)

	sealHash := common.HexToHash("0x112233445566778899aabbccddeeff00112233445566778899aabbccddeeff00")
	var sealArr [32]byte
	copy(sealArr[:], sealHash.Bytes())

	// Precompute 8 reference single hashes
	var ref [8][32]byte
	for i := uint64(0); i < 8; i++ {
		h, err := ctx.CalculateHashWithVM(vm, MiningInput(sealHash, 100+i))
		if err != nil {
			t.Fatalf("CalculateHashWithVM(%d) failed: %v", i, err)
		}
		ref[i] = h
	}

	// 1. count == 1 match on exact boundary target
	targetSingle := reverseToTargetBE(ref[0])
	winNonce, winHash, attempted, found, err := ctx.MineBatchWithVM(vm, sealArr, 100, 1, targetSingle)
	if err != nil || !found || attempted != 1 || winNonce != 100 || winHash != ref[0] {
		t.Fatalf("count=1 match mismatch: nonce=%d found=%v attempted=%d err=%v", winNonce, found, attempted, err)
	}

	// 2. Full miss (zero target) followed by VM cleanliness verification
	var zeroTarget [32]byte
	_, _, attempted, found, err = ctx.MineBatchWithVM(vm, sealArr, 100, 4, zeroTarget)
	if err != nil || found || attempted != 4 {
		t.Fatalf("miss mismatch: found=%v attempted=%d err=%v", found, attempted, err)
	}
	afterHash, err := ctx.CalculateHashWithVM(vm, MiningInput(sealHash, 100))
	if err != nil || afterHash != ref[0] {
		t.Fatalf("VM state corrupted after MineBatchWithVM: err=%v eq=%v", err, bytes.Equal(afterHash[:], ref[0][:]))
	}

	// 3. Find first index k > 0 strictly smaller than all earlier hashes in ref[0..7]
	targetIdx := -1
	for k := 1; k < len(ref); k++ {
		tk := reverseToTargetBE(ref[k])
		earlierMatches := false
		for j := 0; j < k; j++ {
			if HashMeetsTarget(ref[j], tk) {
				earlierMatches = true
				break
			}
		}
		if !earlierMatches {
			targetIdx = k
			break
		}
	}
	if targetIdx < 0 {
		t.Skip("unlikely hash ordering across 8 nonces")
	}

	// Hit mid-batch (or at batch tail if targetIdx == len(ref)-1)
	targetMid := reverseToTargetBE(ref[targetIdx])
	winNonce, winHash, attempted, found, err = ctx.MineBatchWithVM(vm, sealArr, 100, uint32(len(ref)), targetMid)
	if err != nil {
		t.Fatalf("MineBatchWithVM failed: %v", err)
	}
	wantNonce := uint64(100 + targetIdx)
	if !found || winNonce != wantNonce || winHash != ref[targetIdx] || attempted != uint32(targetIdx+1) {
		t.Fatalf("mid-batch mismatch: got found=%v nonce=%d attempted=%d, want found=true nonce=%d attempted=%d",
			found, winNonce, attempted, wantNonce, targetIdx+1)
	}

	// Subsequent hash call on same VM must remain bit-exact after early break
	postBreakHash, err := ctx.CalculateHashWithVM(vm, MiningInput(sealHash, 107))
	if err != nil || postBreakHash != ref[7] {
		t.Fatalf("VM state corrupted after early win break: err=%v", err)
	}
}

func BenchmarkCalculateHashSingle(b *testing.B) {
	ctx, err := NewRxContext([]byte("bench-seed"), false)
	if err != nil {
		b.Fatalf("NewRxContext failed: %v", err)
	}
	defer ctx.Close()
	if !ctx.IncRef() {
		b.Fatal("IncRef failed")
	}
	defer ctx.DecRef()
	vm, err := ctx.AcquireVM()
	if err != nil {
		b.Fatalf("AcquireVM failed: %v", err)
	}
	defer ctx.ReleaseVM(vm)

	blob := MiningInput(common.HexToHash("0x11223344"), 1)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ctx.CalculateHashWithVM(vm, blob); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMineBatch(b *testing.B) {
	ctx, err := NewRxContext([]byte("bench-seed"), false)
	if err != nil {
		b.Fatalf("NewRxContext failed: %v", err)
	}
	defer ctx.Close()
	if !ctx.IncRef() {
		b.Fatal("IncRef failed")
	}
	defer ctx.DecRef()
	vm, err := ctx.AcquireVM()
	if err != nil {
		b.Fatalf("AcquireVM failed: %v", err)
	}
	defer ctx.ReleaseVM(vm)

	var sealArr, zeroTarget [32]byte
	sealArr[0] = 0x11
	const batch = 16
	b.ResetTimer()
	for i := 0; i < b.N; i += batch {
		n := uint32(batch)
		if b.N-i < batch {
			n = uint32(b.N - i)
		}
		if _, _, _, _, err := ctx.MineBatchWithVM(vm, sealArr, uint64(i), n, zeroTarget); err != nil {
			b.Fatal(err)
		}
	}
}

