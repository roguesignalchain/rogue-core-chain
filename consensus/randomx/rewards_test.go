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

	// Era 0: last block (includes EraRemainderWei)
	expectedEra0End := new(big.Int).Add(twentyFiveROGUE, EraRemainderWei)
	if r := CalculateBlockReward(HalvingInterval); r.Cmp(expectedEra0End) != 0 {
		t.Errorf("Block %d reward mismatch: expected %v, got %v", HalvingInterval, expectedEra0End, r)
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
	// Calculate exact total supply across all 32 eras:
	// Sum_{era=0}^{31} ((HalvingInterval-1) * (25 / 2^era) + (25 / 2^era + EraRemainderWei))
	total := new(big.Int)
	for era := uint64(0); era < MaxHalvingEras; era++ {
		reward := new(big.Int).Rsh(InitialBaseReward, uint(era))
		eraTotal := new(big.Int).Mul(big.NewInt(int64(HalvingInterval)), reward)
		eraTotal.Add(eraTotal, EraRemainderWei)
		total.Add(total, eraTotal)
	}

	if total.Cmp(MaxSupplyCap) != 0 {
		t.Fatalf("Total emission %v does not equal exact maximum supply cap of %v (diff: %v)", total, MaxSupplyCap, new(big.Int).Sub(MaxSupplyCap, total))
	}

	// Verify exact convergence to 100,000,000.000000000000000000 ROGUE
	t.Logf("Total calculated max supply over 32 eras: %s Wei (exactly 100,000,000 ROGUE)", total.String())
}
