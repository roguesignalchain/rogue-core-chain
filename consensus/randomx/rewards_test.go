package randomx

import (
	"math/big"
	"testing"
)

func TestBlockRewardHalving(t *testing.T) {
	eightROGUE := new(big.Int).Mul(big.NewInt(8), WeiPerROGUE)
	fourROGUE := new(big.Int).Mul(big.NewInt(4), WeiPerROGUE)
	twoROGUE := new(big.Int).Mul(big.NewInt(2), WeiPerROGUE)

	// Block 0
	if r := CalculateBlockReward(0); r.Sign() != 0 {
		t.Errorf("Block 0 reward should be 0, got %v", r)
	}

	// Era 0: block 1
	if r := CalculateBlockReward(1); r.Cmp(eightROGUE) != 0 {
		t.Errorf("Block 1 reward mismatch: expected %v, got %v", eightROGUE, r)
	}

	// Era 0: last block
	if r := CalculateBlockReward(HalvingInterval); r.Cmp(eightROGUE) != 0 {
		t.Errorf("Block %d reward mismatch: expected %v, got %v", HalvingInterval, eightROGUE, r)
	}

	// Era 1: first block
	if r := CalculateBlockReward(HalvingInterval + 1); r.Cmp(fourROGUE) != 0 {
		t.Errorf("Block %d reward mismatch: expected %v, got %v", HalvingInterval+1, fourROGUE, r)
	}

	// Era 2: first block
	if r := CalculateBlockReward(2*HalvingInterval + 1); r.Cmp(twoROGUE) != 0 {
		t.Errorf("Block %d reward mismatch: expected %v, got %v", 2*HalvingInterval+1, twoROGUE, r)
	}

	// Era 32+
	if r := CalculateBlockReward(32*HalvingInterval + 1); r.Sign() != 0 {
		t.Errorf("Block in era 32 should have 0 reward, got %v", r)
	}
}

func TestSplitReward(t *testing.T) {
	eightROGUE := new(big.Int).Mul(big.NewInt(8), WeiPerROGUE)

	miner, treasury := SplitReward(eightROGUE)

	expectedTreasury := new(big.Int).Div(new(big.Int).Mul(eightROGUE, big.NewInt(5)), big.NewInt(100))
	expectedMiner := new(big.Int).Sub(eightROGUE, expectedTreasury)

	if miner.Cmp(expectedMiner) != 0 {
		t.Errorf("Miner reward mismatch: expected %v, got %v", expectedMiner, miner)
	}
	if treasury.Cmp(expectedTreasury) != 0 {
		t.Errorf("Treasury reward mismatch: expected %v, got %v", expectedTreasury, treasury)
	}

	// Sum should exactly equal 8 ROGUE without dust loss
	sum := new(big.Int).Add(miner, treasury)
	if sum.Cmp(eightROGUE) != 0 {
		t.Errorf("Sum of split rewards %v != total %v", sum, eightROGUE)
	}
}

func TestMaxSupplyCapCompliance(t *testing.T) {
	// Calculate theoretical maximum supply across all 32 eras:
	// Sum_{era=0}^{31} (HalvingInterval * (8 / 2^era))
	total := new(big.Int)
	for era := uint64(0); era < MaxHalvingEras; era++ {
		reward := new(big.Int).Rsh(InitialBaseReward, uint(era))
		eraTotal := new(big.Int).Mul(big.NewInt(int64(HalvingInterval)), reward)
		total.Add(total, eraTotal)
	}

	if total.Cmp(MaxSupplyCap) > 0 {
		t.Fatalf("Total emission %v exceeds maximum supply cap of %v", total, MaxSupplyCap)
	}

	// Check that total is close to 100M (approx 100,000,000 ROGUE)
	// (8 * 6,300,000 * (1 - (1/2)^32) / (1 - 1/2) = 100,800,000)
	// Notice: 8 * 6,300,000 * 2 = 100,800,000 ROGUE (or ~100M ROGUE)
	t.Logf("Total calculated max supply over 32 eras: %s ROGUE", new(big.Int).Div(total, WeiPerROGUE).String())
}
