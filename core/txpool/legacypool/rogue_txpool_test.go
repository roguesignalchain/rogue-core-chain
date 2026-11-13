package legacypool

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/params/vars"
)

func TestRogueLegacyTxOnlyValidation(t *testing.T) {
	pool, key := setupPoolWithConfig(params.RogueChainConfig)
	defer pool.Close()

	signer := types.NewEIP155Signer(params.RogueChainConfig.GetChainID())
	to := common.HexToAddress("0x000000000000000000000000000000000000dead")

	// 1. Legacy (Type 0) transaction must pass validateTxBasics
	legacyTx := types.MustSignNewTx(key, signer, &types.LegacyTx{
		Nonce:    0,
		To:       &to,
		Value:    big.NewInt(1),
		Gas:      21000,
		GasPrice: big.NewInt(vars.GWei),
	})
	if err := pool.validateTxBasics(legacyTx, false); err != nil {
		t.Fatalf("expected LegacyTxType to pass validateTxBasics on RogueChainConfig, got: %v", err)
	}

	// 2. EIP-2930 AccessList (Type 1) transaction must be rejected
	latestSigner := types.LatestSignerForChainID(params.RogueChainConfig.GetChainID())
	alTx := types.MustSignNewTx(key, latestSigner, &types.AccessListTx{
		ChainID:  params.RogueChainConfig.GetChainID(),
		Nonce:    0,
		To:       &to,
		Value:    big.NewInt(1),
		Gas:      21000,
		GasPrice: big.NewInt(vars.GWei),
	})
	if err := pool.validateTxBasics(alTx, false); !errors.Is(err, core.ErrTxTypeNotSupported) {
		t.Fatalf("expected ErrTxTypeNotSupported for AccessListTxType on RogueChainConfig, got: %v", err)
	}

	// 3. EIP-1559 DynamicFee (Type 2) transaction must be rejected
	dynTx := types.MustSignNewTx(key, latestSigner, &types.DynamicFeeTx{
		ChainID:   params.RogueChainConfig.GetChainID(),
		Nonce:     0,
		To:        &to,
		Value:     big.NewInt(1),
		Gas:       21000,
		GasTipCap: big.NewInt(vars.GWei),
		GasFeeCap: big.NewInt(2 * vars.GWei),
	})
	if err := pool.validateTxBasics(dynTx, false); !errors.Is(err, core.ErrTxTypeNotSupported) {
		t.Fatalf("expected ErrTxTypeNotSupported for DynamicFeeTxType on RogueChainConfig, got: %v", err)
	}
}
