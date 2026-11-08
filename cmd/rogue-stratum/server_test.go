package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestConfigAndTarget(t *testing.T) {
	target32, targetHex := ShareDiffTarget(1000)
	if target32 == 0 {
		t.Error("Expected non-zero target32")
	}
	if len(targetHex) != 8 {
		t.Errorf("Expected 8 hex chars for target, got %d (%s)", len(targetHex), targetHex)
	}

	// Verify decoding
	bytes, err := hex.DecodeString(targetHex)
	if err != nil {
		t.Fatalf("Failed to decode target hex: %v", err)
	}
	if len(bytes) != 4 {
		t.Errorf("Expected 4 bytes, got %d", len(bytes))
	}
}

func TestJobCreationAndBlobLayout(t *testing.T) {
	jm := NewJobManager(1000)

	wp := &WorkPackage{
		SealHash:    "0x0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",
		SeedHash:    "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		BlockTarget: "0x00000000ffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
		BlockHeight: "0x64", // 100
	}

	job, err := jm.CreateJob(wp, false)
	if err != nil {
		t.Fatalf("CreateJob failed: %v", err)
	}

	if job.Height != 100 {
		t.Errorf("Expected height 100, got %d", job.Height)
	}

	// Verify 76-byte blob = 152 hex chars
	if len(job.Blob) != 152 {
		t.Fatalf("Expected 152 hex chars for 76-byte blob, got %d", len(job.Blob))
	}

	blobBytes, err := hex.DecodeString(job.Blob)
	if err != nil {
		t.Fatalf("Failed to decode blob: %v", err)
	}
	if len(blobBytes) != 76 {
		t.Fatalf("Expected 76 bytes, got %d", len(blobBytes))
	}

	// Verify SealHash in bytes 0..31
	if blobBytes[0] != 0x01 || blobBytes[31] != 0x20 {
		t.Errorf("SealHash not mapped correctly to blob: %x", blobBytes[:32])
	}

	// Verify bytes 32..38 are zeros
	for i := 32; i < 39; i++ {
		if blobBytes[i] != 0 {
			t.Errorf("Expected 0 at blob[%d], got %d", i, blobBytes[i])
		}
	}

	// Verify bytes 39..42 are zeros (miner nonce placeholder)
	for i := 39; i < 43; i++ {
		if blobBytes[i] != 0 {
			t.Errorf("Expected 0 at blob[%d], got %d", i, blobBytes[i])
		}
	}

	// Verify full nonce reconstruction
	minerNonce := uint32(0x12345678)
	fullNonce := CalculateFullNonce(job.ExtraNonce, minerNonce)
	if uint32(fullNonce) != minerNonce {
		t.Errorf("Lower 32-bits mismatch: got %x, want %x", uint32(fullNonce), minerNonce)
	}
	if uint32(fullNonce>>32) != job.ExtraNonce {
		t.Errorf("Upper 32-bits mismatch: got %x, want %x", uint32(fullNonce>>32), job.ExtraNonce)
	}
}

func TestEndToEndStratumTCP(t *testing.T) {
	// 1. Setup mock Core-Rogue HTTP RPC server
	var submitCalled bool
	var submittedNonce, submittedSeal, submittedDigest string

	mockNode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req JSONRPCRequest
		json.NewDecoder(r.Body).Decode(&req)

		switch req.Method {
		case "eth_getWork":
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": [4]string{
					"0x1111111111111111111111111111111111111111111111111111111111111111",
					"0x2222222222222222222222222222222222222222222222222222222222222222",
					"0x00ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
					"0xa", // Block 10
				},
			}
			json.NewEncoder(w).Encode(resp)

		case "eth_submitWork":
			submitCalled = true
			submittedNonce = req.Params[0].(string)
			submittedSeal = req.Params[1].(string)
			submittedDigest = req.Params[2].(string)

			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  true,
			}
			json.NewEncoder(w).Encode(resp)
		}
	}))
	defer mockNode.Close()

	// 2. Setup and start Stratum Server on random port
	cfg := &Config{
		StratumHost:    "127.0.0.1",
		StratumPort:    0, // Ephemeral port
		NodeRPCURL:     mockNode.URL,
		PollInterval:   50 * time.Millisecond,
		ShareDiff:      1000,
		MaxConnections: 10,
		LogLevel:       "debug",
	}

	server := NewStratumServer(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Listen on ephemeral port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Failed to listen: %v", err)
	}
	server.listener = ln
	serverAddr := ln.Addr().String()

	if err := server.pollWork(); err != nil {
		t.Fatalf("Initial pollWork failed: %v", err)
	}
	go server.workLoop(ctx)
	go server.acceptLoop()
	defer server.Stop()

	// 3. Connect simulated XMRig TCP client
	conn, err := net.Dial("tcp", serverAddr)
	if err != nil {
		t.Fatalf("Failed to connect to Stratum server: %v", err)
	}
	defer conn.Close()

	reader := bufio.NewReader(conn)

	// Send login
	loginReq := `{"id":1,"jsonrpc":"2.0","method":"login","params":{"login":"0x9999999999999999999999999999999999999999.rig1","pass":"x","agent":"XMRig/6.21.0"}}` + "\n"
	conn.Write([]byte(loginReq))

	// Read login response
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("Failed to read login response: %v", err)
	}

	var loginResp StratumOutResponse
	if err := json.Unmarshal(line, &loginResp); err != nil {
		t.Fatalf("Failed to parse login response: %v", err)
	}

	resultMap, ok := loginResp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("Invalid login result type: %T", loginResp.Result)
	}

	sessionID := resultMap["id"].(string)
	jobMap := resultMap["job"].(map[string]interface{})
	jobID := jobMap["job_id"].(string)

	if sessionID == "" || jobID == "" {
		t.Errorf("Expected non-empty sessionID and jobID, got %s, %s", sessionID, jobID)
	}

	// 4. Test keepalive
	keepaliveReq := fmt.Sprintf(`{"id":2,"jsonrpc":"2.0","method":"keepalived","params":{"id":"%s"}}`+"\n", sessionID)
	conn.Write([]byte(keepaliveReq))

	line, err = reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("Failed to read keepalive response: %v", err)
	}
	var keepaliveResp StratumOutResponse
	json.Unmarshal(line, &keepaliveResp)
	kaResult := keepaliveResp.Result.(map[string]interface{})
	if kaResult["status"] != "KEEPALIVED" {
		t.Errorf("Expected KEEPALIVED, got %v", kaResult["status"])
	}

	// 5. Test submit share (valid share meeting difficulty and block target)
	// Low hash meeting diff: ends with bytes 00 00 00 00 (little-endian)
	validHash := "1234567890abcdef1234567890abcdef1234567890abcdef0000000000000000"
	submitReq := fmt.Sprintf(`{"id":3,"jsonrpc":"2.0","method":"submit","params":{"id":"%s","job_id":"%s","nonce":"a1b2c3d4","result":"%s"}}`+"\n",
		sessionID, jobID, validHash)

	conn.Write([]byte(submitReq))

	line, err = reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("Failed to read submit response: %v", err)
	}

	var submitResp StratumOutResponse
	json.Unmarshal(line, &submitResp)
	if submitResp.Error != nil {
		t.Fatalf("Submit returned error: %v", submitResp.Error)
	}

	subResult := submitResp.Result.(map[string]interface{})
	if subResult["status"] != "OK" {
		t.Errorf("Expected OK submit status, got %v", subResult["status"])
	}

	// Wait briefly for asynchronous SubmitBlock goroutine to hit mock node
	time.Sleep(100 * time.Millisecond)

	if !submitCalled {
		t.Error("Expected block submission to Core-Rogue node, but eth_submitWork was not called")
	}

	if submittedNonce == "" {
		t.Error("Expected non-empty submittedNonce")
	}

	if submittedSeal != "0x1111111111111111111111111111111111111111111111111111111111111111" {
		t.Errorf("Seal mismatch: %s", submittedSeal)
	}

	if submittedDigest != "0x"+validHash {
		t.Errorf("Digest mismatch: got %s, want 0x%s", submittedDigest, validHash)
	}
}
