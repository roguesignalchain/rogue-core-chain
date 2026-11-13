package legacypool

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/txpool"
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

	// 4. Unprotected pre-EIP-155 LegacyTx must be rejected on RogueChainConfig
	unprotectedTx := types.MustSignNewTx(key, types.HomesteadSigner{}, &types.LegacyTx{
		Nonce:    0,
		To:       &to,
		Value:    big.NewInt(1),
		Gas:      vars.TxGas,
		GasPrice: big.NewInt(vars.GWei),
	})
	if err := pool.validateTxBasics(unprotectedTx, false); !errors.Is(err, txpool.ErrInvalidSender) {
		t.Fatalf("expected ErrInvalidSender for unprotected pre-EIP-155 LegacyTx on RogueChainConfig, got: %v", err)
	}

	// 5. EIP-3860 initcode size limit must be enforced at block 0 on RogueChainConfig
	oversizedInitCode := make([]byte, vars.MaxInitCodeSize+1)
	oversizedTx := types.MustSignNewTx(key, signer, &types.LegacyTx{
		Nonce:    0,
		Value:    big.NewInt(0),
		Gas:      1_000_000,
		GasPrice: big.NewInt(vars.GWei),
		Data:     oversizedInitCode,
	})
	if err := pool.validateTxBasics(oversizedTx, false); !errors.Is(err, core.ErrMaxInitCodeSizeExceeded) {
		t.Fatalf("expected ErrMaxInitCodeSizeExceeded on RogueChainConfig, got: %v", err)
	}

	// 6. EIP-3860 initcode gas metering (2 gas per 32-byte word) must be enforced in IntrinsicGas check
	// For 1024 zero bytes (32 words): TxGasContractCreation (53000) + 1024*4 (4096) + 32*2 (64) = 57160.
	// Providing 57100 covers legacy intrinsic gas (57096) without EIP-3860 word cost, so must fail.
	initCode := make([]byte, 1024)
	undergassedInitTx := types.MustSignNewTx(key, signer, &types.LegacyTx{
		Nonce:    0,
		Value:    big.NewInt(0),
		Gas:      57100,
		GasPrice: big.NewInt(vars.GWei),
		Data:     initCode,
	})
	if err := pool.validateTxBasics(undergassedInitTx, false); !errors.Is(err, core.ErrIntrinsicGas) {
		t.Fatalf("expected ErrIntrinsicGas for undergassed EIP-3860 contract creation on RogueChainConfig, got: %v", err)
	}
}
