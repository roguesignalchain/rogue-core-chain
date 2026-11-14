package eth

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/params"
)

func TestMakeExtraDataDefault(t *testing.T) {
	if !bytes.Contains(makeExtraData(nil), []byte("CoreGeth")) {
		t.Error("missing extra data default client identifier")
	}
}

func TestMakeExtraDataForChainRandomX(t *testing.T) {
	extra58 := bytes.Repeat([]byte("A"), 58)
	if got := makeExtraDataForChain(extra58, params.RogueChainConfig); len(got) != 58 {
		t.Fatalf("expected 58-byte extra data preserved for RogueChainConfig, got %d", len(got))
	}
	extra64 := bytes.Repeat([]byte("B"), 64)
	if got := makeExtraDataForChain(extra64, params.RogueChainConfig); len(got) != 64 {
		t.Fatalf("expected 64-byte extra data preserved for RogueChainConfig, got %d", len(got))
	}
	extra65 := bytes.Repeat([]byte("C"), 65)
	if got := makeExtraDataForChain(extra65, params.RogueChainConfig); got != nil {
		t.Fatalf("expected 65-byte extra data rejected for RogueChainConfig, got len %d", len(got))
	}
	if got := makeExtraDataForChain(extra58, nil); got != nil {
		t.Fatalf("expected 58-byte extra data rejected for default 32-byte limit, got len %d", len(got))
	}
}
