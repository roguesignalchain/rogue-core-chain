package core

import (
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/params/vars"
	"github.com/holiman/uint256"
)

// TestRogueAccessListResetAndWarmCoinbase verifies that on RogueChainConfig
// (which enables EIP-2929 and EIP-3651 from block 0 without EIP-2930):
// 1. StateDB.Prepare resets the EIP-2929 access list between transactions in the same block.
// 2. StateDB.Prepare pre-warms COINBASE per EIP-3651.
func TestRogueAccessListResetAndWarmCoinbase(t *testing.T) {
	cfg := params.RogueChainConfig
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	sender := crypto.PubkeyToAddress(key.PublicKey)
	coinbase := common.HexToAddress("0xdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	coldTarget := common.HexToAddress("0x000000000000000000000000000000000000cafe")

	// Bytecode 1: PUSH1 0x00 | SLOAD | POP | STOP
	// If access list leaks between txs in the same block, the 2nd tx would pay 100 gas instead of 2100.
	sloadProbe := common.HexToAddress("0x000000000000000000000000000000000000aa01")
	sloadCode := []byte{0x60, 0x00, 0x54, 0x50, 0x00}

	// Bytecode 2: COINBASE | BALANCE | POP | STOP (warms coinbase under EIP-3651)
	coinbaseProbe := common.HexToAddress("0x000000000000000000000000000000000000aa02")
	coinbaseCode := []byte{0x41, 0x31, 0x50, 0x00}

	// Bytecode 3: PUSH20 <coldTarget> | BALANCE | POP | STOP (cold address baseline)
	coldProbe := common.HexToAddress("0x000000000000000000000000000000000000aa03")
	coldCode := make([]byte, 0, 24)
	coldCode = append(coldCode, 0x73)
	coldCode = append(coldCode, coldTarget.Bytes()...)
	coldCode = append(coldCode, 0x31, 0x50, 0x00)

	statedb, err := state.New(types.EmptyRootHash, state.NewDatabase(rawdb.NewMemoryDatabase()), nil)
	if err != nil {
		t.Fatalf("failed to create statedb: %v", err)
	}
	statedb.AddBalance(sender, new(uint256.Int).Mul(uint256.NewInt(100), uint256.NewInt(vars.Ether)))
	statedb.SetCode(sloadProbe, sloadCode)
	statedb.SetCode(coinbaseProbe, coinbaseCode)
	statedb.SetCode(coldProbe, coldCode)
	statedb.CreateAccount(coinbase)
	statedb.CreateAccount(coldTarget)

	header := &types.Header{
		Number:     big.NewInt(1),
		Time:       1700000030,
		Difficulty: big.NewInt(1),
		GasLimit:   60_000_000,
		Coinbase:   coinbase,
	}
	signer := types.MakeSigner(cfg, header.Number, header.Time)
	gp := new(GasPool).AddGas(header.GasLimit)
	var usedGas uint64

	signCall := func(nonce uint64, to common.Address) *types.Transaction {
		return types.MustSignNewTx(key, signer, &types.LegacyTx{
			Nonce:    nonce,
			To:       &to,
			Value:    big.NewInt(0),
			Gas:      100_000,
			GasPrice: big.NewInt(vars.GWei),
		})
	}

	tx1 := signCall(0, sloadProbe)
	statedb.SetTxContext(tx1.Hash(), 0)
	rec1, err := ApplyTransaction(cfg, nil, &coinbase, gp, statedb, header, tx1, &usedGas, vm.Config{})
	if err != nil {
		t.Fatalf("tx1 failed: %v", err)
	}

	tx2 := signCall(1, sloadProbe)
	statedb.SetTxContext(tx2.Hash(), 1)
	rec2, err := ApplyTransaction(cfg, nil, &coinbase, gp, statedb, header, tx2, &usedGas, vm.Config{})
	if err != nil {
		t.Fatalf("tx2 failed: %v", err)
	}

	if rec1.GasUsed != rec2.GasUsed {
		t.Fatalf("EIP-2929 access list leaked across transactions in same block: tx1 gas=%d, tx2 gas=%d", rec1.GasUsed, rec2.GasUsed)
	}
	// TxGas (21000) + PUSH1 (3) + cold SLOAD (2100) + POP (2) + STOP (0) = 23105
	if rec1.GasUsed != 23105 {
		t.Fatalf("unexpected cold SLOAD tx gas on RogueChainConfig: got %d, want 23105", rec1.GasUsed)
	}

	txCoinbase := signCall(2, coinbaseProbe)
	statedb.SetTxContext(txCoinbase.Hash(), 2)
	recCoinbase, err := ApplyTransaction(cfg, nil, &coinbase, gp, statedb, header, txCoinbase, &usedGas, vm.Config{})
	if err != nil {
		t.Fatalf("txCoinbase failed: %v", err)
	}

	txCold := signCall(3, coldProbe)
	statedb.SetTxContext(txCold.Hash(), 3)
	recCold, err := ApplyTransaction(cfg, nil, &coinbase, gp, statedb, header, txCold, &usedGas, vm.Config{})
	if err != nil {
		t.Fatalf("txCold failed: %v", err)
	}

	// COINBASE costs 2 gas + warm BALANCE 100 vs PUSH20 3 gas + cold BALANCE 2600 => diff = 2501 gas.
	if gotDiff := recCold.GasUsed - recCoinbase.GasUsed; gotDiff != 2501 {
		t.Fatalf("expected EIP-3651 warm coinbase savings of 2501 gas (coinbase=%d, cold=%d), got diff=%d",
			recCoinbase.GasUsed, recCold.GasUsed, gotDiff)
	}
}

// TestRogueMandatoryEIP155BlockProcessing verifies that unprotected (Homestead) and
// foreign-chain transactions are unconditionally rejected by consensus block execution
// on RogueChainConfig from Block 1 onward.
func TestRogueMandatoryEIP155BlockProcessing(t *testing.T) {
	cfg := params.RogueChainConfig
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}
	sender := crypto.PubkeyToAddress(key.PublicKey)
	recipient := common.HexToAddress("0x000000000000000000000000000000000000dead")

	statedb, err := state.New(types.EmptyRootHash, state.NewDatabase(rawdb.NewMemoryDatabase()), nil)
	if err != nil {
		t.Fatalf("failed to create statedb: %v", err)
	}
	statedb.AddBalance(sender, new(uint256.Int).Mul(uint256.NewInt(10), uint256.NewInt(vars.Ether)))

	header := &types.Header{
		Number:     big.NewInt(1),
		Time:       1700000030,
		Difficulty: big.NewInt(1),
		GasLimit:   60_000_000,
		Coinbase:   recipient,
	}
	gp := new(GasPool).AddGas(header.GasLimit)
	var usedGas uint64

	// 1. Unprotected pre-EIP-155 tx must fail consensus execution
	unprotected := types.MustSignNewTx(key, types.HomesteadSigner{}, &types.LegacyTx{
		Nonce:    0,
		To:       &recipient,
		Value:    big.NewInt(1000),
		Gas:      vars.TxGas,
		GasPrice: big.NewInt(vars.GWei),
	})
	if _, err := ApplyTransaction(cfg, nil, &recipient, gp, statedb, header, unprotected, &usedGas, vm.Config{}); !errors.Is(err, types.ErrInvalidChainId) {
		t.Fatalf("expected ErrInvalidChainId for unprotected tx in block execution, got: %v", err)
	}

	// 2. Wrong-chain EIP-155 tx must also fail consensus execution
	wrongChain := types.MustSignNewTx(key, types.NewEIP155Signer(big.NewInt(1)), &types.LegacyTx{
		Nonce:    0,
		To:       &recipient,
		Value:    big.NewInt(1000),
		Gas:      vars.TxGas,
		GasPrice: big.NewInt(vars.GWei),
	})
	if _, err := ApplyTransaction(cfg, nil, &recipient, gp, statedb, header, wrongChain, &usedGas, vm.Config{}); !errors.Is(err, types.ErrInvalidChainId) {
		t.Fatalf("expected ErrInvalidChainId for foreign-chain tx in block execution, got: %v", err)
	}

	// 3. Valid EIP-155 tx (ChainID 800055) must pass
	valid := types.MustSignNewTx(key, types.LatestSigner(cfg), &types.LegacyTx{
		Nonce:    0,
		To:       &recipient,
		Value:    big.NewInt(1000),
		Gas:      vars.TxGas,
		GasPrice: big.NewInt(vars.GWei),
	})
	statedb.SetTxContext(valid.Hash(), 0)
	if _, err := ApplyTransaction(cfg, nil, &recipient, gp, statedb, header, valid, &usedGas, vm.Config{}); err != nil {
		t.Fatalf("expected valid EIP-155 tx to pass consensus execution, got: %v", err)
	}
}
