package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"
)

// StratumInMessage is incoming message from miner.
type StratumInMessage struct {
	ID      interface{}     `json:"id"`
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// LoginParams contains parameters for login method.
type LoginParams struct {
	Login string `json:"login"`
	Pass  string `json:"pass"`
	Agent string `json:"agent"`
	RigID string `json:"rigid"`
}

// SubmitParams contains parameters for submit method.
type SubmitParams struct {
	ID     string `json:"id"`
	JobID  string `json:"job_id"`
	Nonce  string `json:"nonce"`
	Result string `json:"result"`
}

// StratumOutResponse is generic success/error response.
type StratumOutResponse struct {
	ID      interface{} `json:"id"`
	JSONRPC string      `json:"jsonrpc"`
	Result  interface{} `json:"result,omitempty"`
	Error   interface{} `json:"error"`
}

// LoginResult is result payload for successful login.
type LoginResult struct {
	ID     string      `json:"id"`
	Job    *StratumJob `json:"job"`
	Status string      `json:"status"`
}

// SubmitResult is result payload for accepted share.
type SubmitResult struct {
	Status string `json:"status"`
}

// StratumNotification is unrequested push message (e.g. new job).
type StratumNotification struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  *StratumJob `json:"params"`
}

// Session represents an active miner TCP connection.
type Session struct {
	id         string
	conn       net.Conn
	reader     *bufio.Reader
	mu         sync.Mutex
	server     *StratumServer
	wallet     string
	worker     string
	agent      string
	connected  time.Time
	accepted   uint64
	rejected   uint64
	lastActive time.Time
}

// GenerateSessionID produces a random 16-hex-character session identifier.
func GenerateSessionID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// NewSession creates and initializes a miner session.
func NewSession(conn net.Conn, server *StratumServer) *Session {
	return &Session{
		id:         GenerateSessionID(),
		conn:       conn,
		reader:     bufio.NewReader(conn),
		server:     server,
		connected:  time.Now(),
		lastActive: time.Now(),
	}
}

// Send sends a serialized JSON string terminated with newline to the miner.
func (s *Session) Send(v interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = s.conn.Write(data)
	return err
}

// PushJob notifies the miner of a new job.
func (s *Session) PushJob(job *StratumJob) error {
	return s.Send(StratumNotification{
		JSONRPC: "2.0",
		Method:  "job",
		Params:  job,
	})
}

// HandleLoop reads lines and processes incoming Stratum requests until disconnect.
func (s *Session) HandleLoop() {
	defer func() {
		s.conn.Close()
		s.server.RemoveSession(s.id)
		log.Printf("[Stratum] Disconnected session %s (%s / %s)", s.id, s.wallet, s.worker)
	}()

	scanner := bufio.NewScanner(s.reader)
	// Maximum Stratum message 64KB
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, len(buf))

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		s.lastActive = time.Now()

		var msg StratumInMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			log.Printf("[Stratum] Failed to parse message from %s: %v", s.conn.RemoteAddr(), err)
			continue
		}

		s.handleMethod(&msg)
	}
}

func (s *Session) handleMethod(msg *StratumInMessage) {
	switch msg.Method {
	case "login":
		s.handleLogin(msg)
	case "submit":
		s.handleSubmit(msg)
	case "keepalived":
		s.handleKeepalive(msg)
	default:
		// Unknown method
		s.Send(StratumOutResponse{
			ID:      msg.ID,
			JSONRPC: "2.0",
			Error: map[string]interface{}{
				"code":    -32601,
				"message": fmt.Sprintf("Method not found: %s", msg.Method),
			},
		})
	}
}

func (s *Session) handleLogin(msg *StratumInMessage) {
	var params LoginParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		s.Send(StratumOutResponse{
			ID:      msg.ID,
			JSONRPC: "2.0",
			Error: map[string]interface{}{
				"code":    -32602,
				"message": "Invalid params for login",
			},
		})
		return
	}

	// Extract wallet address and rig id: "0xAddress.worker1"
	parts := strings.Split(params.Login, ".")
	s.wallet = parts[0]
	if len(parts) > 1 {
		s.worker = parts[1]
	} else if params.RigID != "" {
		s.worker = params.RigID
	} else {
		s.worker = "default"
	}
	s.agent = params.Agent

	log.Printf("[Stratum] Miner login: %s.%s (%s) from %s", s.wallet, s.worker, s.agent, s.conn.RemoteAddr())

	currentJob := s.server.jobs.CurrentJob()
	if currentJob == nil {
		s.Send(StratumOutResponse{
			ID:      msg.ID,
			JSONRPC: "2.0",
			Error: map[string]interface{}{
				"code":    -1,
				"message": "No mining work currently available from node",
			},
		})
		return
	}

	s.Send(StratumOutResponse{
		ID:      msg.ID,
		JSONRPC: "2.0",
		Result: LoginResult{
			ID:     s.id,
			Job:    currentJob,
			Status: "OK",
		},
		Error: nil,
	})
}

func (s *Session) handleSubmit(msg *StratumInMessage) {
	var params SubmitParams
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		s.Send(StratumOutResponse{
			ID:      msg.ID,
			JSONRPC: "2.0",
			Error: map[string]interface{}{
				"code":    -32602,
				"message": "Invalid params for submit",
			},
		})
		return
	}

	job := s.server.jobs.GetJob(params.JobID)
	if job == nil {
		s.rejected++
		s.Send(StratumOutResponse{
			ID:      msg.ID,
			JSONRPC: "2.0",
			Error: map[string]interface{}{
				"code":    -1,
				"message": "Job not found or expired",
			},
		})
		return
	}

	isShareValid, isBlockCandidate, err := s.server.jobs.VerifyShare(job, params.Nonce, params.Result)
	if err != nil || !isShareValid {
		s.rejected++
		log.Printf("[Stratum] Share rejected from %s.%s: %v", s.wallet, s.worker, err)
		s.Send(StratumOutResponse{
			ID:      msg.ID,
			JSONRPC: "2.0",
			Error: map[string]interface{}{
				"code":    -1,
				"message": "Invalid share",
			},
		})
		return
	}

	s.accepted++
	// Confirm share acceptance to miner
	s.Send(StratumOutResponse{
		ID:      msg.ID,
		JSONRPC: "2.0",
		Result: SubmitResult{
			Status: "OK",
		},
		Error: nil,
	})

	log.Printf("[Stratum] Share accepted from %s.%s (diff: %d, job: %s)", s.wallet, s.worker, s.server.config.ShareDiff, params.JobID)

	// If the share meets full network block difficulty, broadcast solution to node!
	if isBlockCandidate {
		log.Printf("=================================================================")
		log.Printf("🎉 BLOCK FOUND! Miner %s.%s solved block at height %d!", s.wallet, s.worker, job.Height)
		log.Printf("=================================================================")

		go s.server.SubmitBlock(job, params.Nonce, params.Result)
	}
}

func (s *Session) handleKeepalive(msg *StratumInMessage) {
	s.Send(StratumOutResponse{
		ID:      msg.ID,
		JSONRPC: "2.0",
		Result: map[string]interface{}{
			"status": "KEEPALIVED",
		},
		Error: nil,
	})
}
