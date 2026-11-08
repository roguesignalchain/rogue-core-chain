package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// RPCClient interacts with the Core-Rogue node via HTTP JSON-RPC.
type RPCClient struct {
	endpoint   string
	httpClient *http.Client
	requestID  uint64
}

// WorkPackage represents work returned by eth_getWork.
type WorkPackage struct {
	SealHash    string // result[0]: 32 bytes hex
	SeedHash    string // result[1]: 32 bytes hex RandomX seed
	BlockTarget string // result[2]: 32 bytes hex 256-bit target
	BlockHeight string // result[3]: hex block number
}

// JSONRPCRequest represents standard JSON-RPC 2.0 payload.
type JSONRPCRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      uint64        `json:"id"`
}

// JSONRPCResponse represents standard JSON-RPC 2.0 response.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError holds code and error message.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// NewRPCClient initializes an HTTP client for Node communication.
func NewRPCClient(endpoint string) *RPCClient {
	return &RPCClient{
		endpoint: endpoint,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// Call executes an arbitrary JSON-RPC call against core-rogue.
func (c *RPCClient) Call(method string, params ...interface{}) (*JSONRPCResponse, error) {
	id := atomic.AddUint64(&c.requestID, 1)

	p := params
	if p == nil {
		p = []interface{}{}
	}

	reqBody, err := json.Marshal(JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  p,
		ID:      id,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := c.httpClient.Post(c.endpoint, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("RPC POST failed: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var rpcResp JSONRPCResponse
	if err := json.Unmarshal(bodyBytes, &rpcResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON-RPC: %w (raw: %s)", err, string(bodyBytes))
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("RPC returned error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	return &rpcResp, nil
}

// GetWork fetches the current mining work from core-rogue (eth_getWork).
func (c *RPCClient) GetWork() (*WorkPackage, error) {
	resp, err := c.Call("eth_getWork")
	if err != nil {
		return nil, err
	}

	var work [4]string
	if err := json.Unmarshal(resp.Result, &work); err != nil {
		return nil, fmt.Errorf("failed to unmarshal work package: %w", err)
	}

	return &WorkPackage{
		SealHash:    work[0],
		SeedHash:    work[1],
		BlockTarget: work[2],
		BlockHeight: work[3],
	}, nil
}

// SubmitWork submits a solved block to core-rogue (eth_submitWork).
func (c *RPCClient) SubmitWork(nonceHex, sealHashHex, digestHex string) (bool, error) {
	resp, err := c.Call("eth_submitWork", nonceHex, sealHashHex, digestHex)
	if err != nil {
		return false, err
	}

	var success bool
	if err := json.Unmarshal(resp.Result, &success); err != nil {
		return false, fmt.Errorf("failed to unmarshal submitWork response: %w", err)
	}

	return success, nil
}
