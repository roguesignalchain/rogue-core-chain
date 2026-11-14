package utils

import (
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/params/vars"
	"github.com/urfave/cli/v2"
)

func newRogueFlagTestContext(t *testing.T, args []string) *cli.Context {
	t.Helper()
	var captured *cli.Context
	app := &cli.App{
		Flags: []cli.Flag{
			RogueFlag,
			NetworkIdFlag,
			MainnetFlag,
			ClassicFlag,
			MordorFlag,
			SepoliaFlag,
			MintMeFlag,
			HoleskyFlag,
			DeveloperFlag,
			DeveloperPoWFlag,
			ListenPortFlag,
			HTTPPortFlag,
			WSPortFlag,
			AuthPortFlag,
			DataDirFlag,
		},
		Action: func(ctx *cli.Context) error {
			captured = ctx
			return nil
		},
	}
	if err := app.Run(append([]string{"core-rogue"}, args...)); err != nil {
		t.Fatalf("app.Run(%v) failed: %v", args, err)
	}
	return captured
}

func TestIsRogueNetworkAndPorts(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{"rogue flag", []string{"--rogue"}, true},
		{"networkid 800055", []string{"--networkid", "800055"}, true},
		{"classic with networkid 800055", []string{"--classic", "--networkid", "800055"}, false},
		{"mainnet with networkid 800055", []string{"--mainnet", "--networkid", "800055"}, false},
		{"dev with networkid 800055", []string{"--dev", "--networkid", "800055"}, false},
		{"other networkid", []string{"--networkid", "1"}, false},
	}
	for _, tc := range tests {
		ctx := newRogueFlagTestContext(t, tc.args)
		if got := isRogueNetwork(ctx); got != tc.want {
			t.Errorf("%s: isRogueNetwork() = %v, want %v", tc.name, got, tc.want)
		}
	}

	// --networkid 800055 without --rogue must configure Rogue ports, datadir, and genesis.
	ctx := newRogueFlagTestContext(t, []string{"--networkid", "800055"})
	cfg := node.Config{DataDir: vars.DefaultDataDir()}
	SetNodeConfig(ctx, &cfg)
	if cfg.P2P.ListenAddr != ":38888" || cfg.HTTPPort != 38845 || cfg.WSPort != 38846 || cfg.AuthPort != 38851 {
		t.Fatalf("unexpected ports for --networkid 800055: listen=%s http=%d ws=%d auth=%d",
			cfg.P2P.ListenAddr, cfg.HTTPPort, cfg.WSPort, cfg.AuthPort)
	}
	if filepath.Base(cfg.DataDir) != "rogue" {
		t.Fatalf("expected datadir suffix 'rogue' for --networkid 800055, got %q", cfg.DataDir)
	}
	if genesis := genesisForCtxChainConfig(ctx); genesis == nil || genesis.Config.GetChainID().Cmp(params.RogueChainConfig.GetChainID()) != 0 {
		t.Fatalf("expected RogueChainConfig genesis for --networkid 800055, got %+v", genesis)
	}

	// Explicit --authrpc.port must be respected even when --rogue is active.
	ctxCustomAuth := newRogueFlagTestContext(t, []string{"--rogue", "--authrpc.port", "9551"})
	var cfgCustom node.Config
	SetNodeConfig(ctxCustomAuth, &cfgCustom)
	if cfgCustom.AuthPort != 9551 {
		t.Fatalf("expected explicit --authrpc.port 9551 to be respected, got %d", cfgCustom.AuthPort)
	}
}
