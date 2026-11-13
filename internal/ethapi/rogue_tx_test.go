package ethapi

import (
	"context"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/consensus/randomx"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/params/types/genesisT"
	"github.com/ethereum/go-ethereum/params/vars"
	"github.com/holiman/uint256"
)

type rogueTxTestBackend struct {
	*testBackend
	sentTxs []*types.Transaction
}

func (b *rogueTxTestBackend) SendTx(ctx context.Context, signedTx *types.Transaction) error {
	b.sentTxs = append(b.sentTxs, signedTx)
	return nil
}

func (b *rogueTxTestBackend) UnprotectedAllowed() bool {
	return true
}

func TestSendRawTransactionRogueLegacyOnly(t *testing.T) {
	t.Parallel()

	accounts := newAccounts(2)
	genesis := &genesisT.Genesis{
		Config:   params.RogueChainConfig,
		GasLimit: 50000000,
		Alloc: genesisT.GenesisAlloc{
			accounts[0].addr: {Balance: new(big.Int).Mul(big.NewInt(10), big.NewInt(vars.Ether))},
		},
	}

	engine := randomx.New(randomx.Config{FastMode: false, Threads: 1})
	defer engine.Close()

	backend := &rogueTxTestBackend{testBackend: newTestBackend(t, 0, genesis, engine, nil)}
	txAPI := NewTransactionAPI(backend, new(AddrLocker))

	chainID := params.RogueChainConfig.GetChainID()
	signer := types.NewCancunSigner(chainID)

	// 1. Type 1 (AccessListTx) must be rejected
	tx1, err := types.SignTx(types.NewTx(&types.AccessListTx{
		ChainID:  chainID,
		Nonce:    0,
		To:       &accounts[1].addr,
		Value:    big.NewInt(1000),
		Gas:      vars.TxGas,
		GasPrice: big.NewInt(vars.GWei),
	}), signer, accounts[0].key)
	if err != nil {
		t.Fatalf("failed to sign AccessListTx: %v", err)
	}
	raw1, _ := tx1.MarshalBinary()
	if _, err := txAPI.SendRawTransaction(context.Background(), hexutil.Bytes(raw1)); err == nil || !strings.Contains(err.Error(), "not supported on Rogue Signal") {
		t.Fatalf("expected AccessListTx rejection on Rogue Signal, got %v", err)
	}

	// 2. Type 2 (DynamicFeeTx) must be rejected
	tx2, err := types.SignTx(types.NewTx(&types.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     0,
		To:        &accounts[1].addr,
		Value:     big.NewInt(1000),
		Gas:       vars.TxGas,
		GasTipCap: big.NewInt(vars.GWei),
		GasFeeCap: big.NewInt(2 * vars.GWei),
	}), signer, accounts[0].key)
	if err != nil {
		t.Fatalf("failed to sign DynamicFeeTx: %v", err)
	}
	raw2, _ := tx2.MarshalBinary()
	if _, err := txAPI.SendRawTransaction(context.Background(), hexutil.Bytes(raw2)); err == nil || !strings.Contains(err.Error(), "not supported on Rogue Signal") {
		t.Fatalf("expected DynamicFeeTx rejection on Rogue Signal, got %v", err)
	}

	// 3. Type 3 (BlobTx) must be rejected
	tx3, err := types.SignTx(types.NewTx(&types.BlobTx{
		ChainID:   uint256.MustFromBig(chainID),
		Nonce:     0,
		To:        accounts[1].addr,
		Value:     uint256.NewInt(1000),
		Gas:       vars.TxGas,
		GasTipCap: uint256.NewInt(vars.GWei),
		GasFeeCap: uint256.NewInt(2 * vars.GWei),
		BlobHashes: []common.Hash{
			common.HexToHash("0x0100000000000000000000000000000000000000000000000000000000000000"),
		},
	}), types.NewCancunSigner(chainID), accounts[0].key)
	if err != nil {
		t.Fatalf("failed to sign BlobTx: %v", err)
	}
	raw3, _ := tx3.MarshalBinary()
	if _, err := txAPI.SendRawTransaction(context.Background(), hexutil.Bytes(raw3)); err == nil || !strings.Contains(err.Error(), "not supported on Rogue Signal") {
		t.Fatalf("expected BlobTx rejection on Rogue Signal, got %v", err)
	}

	// 4. LegacyTx signed with wrong ChainID (replay protection) must be rejected
	wrongSigner := types.NewEIP155Signer(big.NewInt(1))
	txWrongChain, err := types.SignTx(types.NewTx(&types.LegacyTx{
		Nonce:    0,
		To:       &accounts[1].addr,
		Value:    big.NewInt(1000),
		Gas:      vars.TxGas,
		GasPrice: big.NewInt(vars.GWei),
	}), wrongSigner, accounts[0].key)
	if err != nil {
		t.Fatalf("failed to sign wrong-chain LegacyTx: %v", err)
	}
	rawWrong, _ := txWrongChain.MarshalBinary()
	if _, err := txAPI.SendRawTransaction(context.Background(), hexutil.Bytes(rawWrong)); err == nil {
		t.Fatal("expected LegacyTx with wrong ChainID to be rejected")
	}

	// 4b. Unprotected pre-EIP-155 LegacyTx must be rejected even if UnprotectedAllowed() returns true
	txUnprotected, err := types.SignTx(types.NewTx(&types.LegacyTx{
		Nonce:    0,
		To:       &accounts[1].addr,
		Value:    big.NewInt(1000),
		Gas:      vars.TxGas,
		GasPrice: big.NewInt(vars.GWei),
	}), types.HomesteadSigner{}, accounts[0].key)
	if err != nil {
		t.Fatalf("failed to sign unprotected LegacyTx: %v", err)
	}
	rawUnprotected, _ := txUnprotected.MarshalBinary()
	if _, err := txAPI.SendRawTransaction(context.Background(), hexutil.Bytes(rawUnprotected)); err == nil || !strings.Contains(err.Error(), "replay-protected (EIP-155)") {
		t.Fatalf("expected unprotected LegacyTx rejection on Rogue Signal, got %v", err)
	}

	// 5. Valid EIP-155 LegacyTx (ChainID 800055) must be accepted
	eip155Signer := types.NewEIP155Signer(chainID)
	txValid, err := types.SignTx(types.NewTx(&types.LegacyTx{
		Nonce:    0,
		To:       &accounts[1].addr,
		Value:    big.NewInt(1000),
		Gas:      vars.TxGas,
		GasPrice: big.NewInt(vars.GWei),
	}), eip155Signer, accounts[0].key)
	if err != nil {
		t.Fatalf("failed to sign valid LegacyTx: %v", err)
	}
	rawValid, _ := txValid.MarshalBinary()
	hash, err := txAPI.SendRawTransaction(context.Background(), hexutil.Bytes(rawValid))
	if err != nil {
		t.Fatalf("expected valid LegacyTx to be accepted, got %v", err)
	}
	if hash != txValid.Hash() {
		t.Fatalf("returned tx hash mismatch: have %s, want %s", hash.Hex(), txValid.Hash().Hex())
	}
}
