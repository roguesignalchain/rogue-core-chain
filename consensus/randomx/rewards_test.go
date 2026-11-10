package randomx

import (
	"math/big"
	"testing"
)

func TestBlockRewardHalving(t *testing.T) {
	twentyFiveROGUE := new(big.Int).Mul(big.NewInt(25), WeiPerROGUE)
	twelvePointFiveROGUE := new(big.Int).Rsh(twentyFiveROGUE, 1)
	sixPointTwoFiveROGUE := new(big.Int).Rsh(twentyFiveROGUE, 2)

	// Block 0
	if r := CalculateBlockReward(0); r.Sign() != 0 {
		t.Errorf("Block 0 reward should be 0, got %v", r)
	}

	// Era 0: block 1
	if r := CalculateBlockReward(1); r.Cmp(twentyFiveROGUE) != 0 {
		t.Errorf("Block 1 reward mismatch: expected %v, got %v", twentyFiveROGUE, r)
	}

	// Era 0: last block
	if r := CalculateBlockReward(HalvingInterval); r.Cmp(twentyFiveROGUE) != 0 {
		t.Errorf("Block %d reward mismatch: expected %v, got %v", HalvingInterval, twentyFiveROGUE, r)
	}

	// Era 1: first block
	if r := CalculateBlockReward(HalvingInterval + 1); r.Cmp(twelvePointFiveROGUE) != 0 {
		t.Errorf("Block %d reward mismatch: expected %v, got %v", HalvingInterval+1, twelvePointFiveROGUE, r)
	}

	// Era 2: first block
	if r := CalculateBlockReward(2*HalvingInterval + 1); r.Cmp(sixPointTwoFiveROGUE) != 0 {
		t.Errorf("Block %d reward mismatch: expected %v, got %v", 2*HalvingInterval+1, sixPointTwoFiveROGUE, r)
	}

	// Era 32+
	if r := CalculateBlockReward(32*HalvingInterval + 1); r.Sign() != 0 {
		t.Errorf("Block in era 32 should have 0 reward, got %v", r)
	}
}

func TestSplitReward(t *testing.T) {
	twentyFiveROGUE := new(big.Int).Mul(big.NewInt(25), WeiPerROGUE)

	miner, treasury := SplitReward(twentyFiveROGUE)

	expectedTreasury := new(big.Int).Div(new(big.Int).Mul(twentyFiveROGUE, big.NewInt(5)), big.NewInt(100))
	expectedMiner := new(big.Int).Sub(twentyFiveROGUE, expectedTreasury)

	if miner.Cmp(expectedMiner) != 0 {
		t.Errorf("Miner reward mismatch: expected %v, got %v", expectedMiner, miner)
	}
	if treasury.Cmp(expectedTreasury) != 0 {
		t.Errorf("Treasury reward mismatch: expected %v, got %v", expectedTreasury, treasury)
	}

	// Sum should exactly equal 25 ROGUE without dust loss
	sum := new(big.Int).Add(miner, treasury)
	if sum.Cmp(twentyFiveROGUE) != 0 {
		t.Errorf("Sum of split rewards %v != total %v", sum, twentyFiveROGUE)
	}
}

func TestMaxSupplyCapCompliance(t *testing.T) {
	// Calculate theoretical maximum supply across all 32 eras:
	// Sum_{era=0}^{31} (HalvingInterval * (25 / 2^era))
	total := new(big.Int)
	for era := uint64(0); era < MaxHalvingEras; era++ {
		reward := new(big.Int).Rsh(InitialBaseReward, uint(era))
		eraTotal := new(big.Int).Mul(big.NewInt(int64(HalvingInterval)), reward)
		total.Add(total, eraTotal)
	}

	if total.Cmp(MaxSupplyCap) > 0 {
		t.Fatalf("Total emission %v exceeds maximum supply cap of %v", total, MaxSupplyCap)
	}

	// Check that total converges to 100,000,000 ROGUE (25 * 2,000,000 * 2 = 100,000,000 ROGUE)
	t.Logf("Total calculated max supply over 32 eras: %s ROGUE", new(big.Int).Div(total, WeiPerROGUE).String())
}
