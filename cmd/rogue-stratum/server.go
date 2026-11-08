package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"sync"
	"time"
)

// StratumServer orchestrates TCP miner connections, node polling, and block submissions.
type StratumServer struct {
	config    *Config
	rpc       *RPCClient
	jobs      *JobManager
	sessions  map[string]*Session
	sessMu    sync.RWMutex
	listener  net.Listener
	stopCh    chan struct{}
	closeOnce sync.Once

	lastSealHash string
}

// NewStratumServer creates a new server instance.
func NewStratumServer(cfg *Config) *StratumServer {
	return &StratumServer{
		config:   cfg,
		rpc:      NewRPCClient(cfg.NodeRPCURL),
		jobs:     NewJobManager(cfg.ShareDiff),
		sessions: make(map[string]*Session),
		stopCh:   make(chan struct{}),
	}
}

// Start begins listening for miners and polling work from the node.
func (s *StratumServer) Start(ctx context.Context) error {
	addr := s.config.ListenAddr()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to bind stratum listener on %s: %w", addr, err)
	}
	s.listener = ln

	log.Printf("[Stratum] Server listening on %s (fixed diff: %d)", addr, s.config.ShareDiff)
	log.Printf("[Stratum] Upstream Node RPC: %s (polling every %v)", s.config.NodeRPCURL, s.config.PollInterval)

	// Fetch initial work before accepting connections
	if err := s.pollWork(); err != nil {
		log.Printf("[Stratum] Warning: initial work fetch failed: %v (retrying in background)", err)
	}

	// Start work polling loop
	go s.workLoop(ctx)

	// Start connection accept loop
	go s.acceptLoop()

	return nil
}

// Stop gracefully terminates the server.
func (s *StratumServer) Stop() {
	s.closeOnce.Do(func() {
		close(s.stopCh)
		if s.listener != nil {
			s.listener.Close()
		}

		s.sessMu.Lock()
		for _, sess := range s.sessions {
			sess.conn.Close()
		}
		s.sessions = make(map[string]*Session)
		s.sessMu.Unlock()

		log.Printf("[Stratum] Server stopped cleanly")
	})
}

func (s *StratumServer) acceptLoop() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.stopCh:
				return
			default:
				log.Printf("[Stratum] Accept error: %v", err)
				continue
			}
		}

		s.sessMu.Lock()
		if len(s.sessions) >= s.config.MaxConnections {
			s.sessMu.Unlock()
			log.Printf("[Stratum] Max connections reached (%d), rejecting %s", s.config.MaxConnections, conn.RemoteAddr())
			conn.Close()
			continue
		}

		sess := NewSession(conn, s)
		s.sessions[sess.id] = sess
		s.sessMu.Unlock()

		go sess.HandleLoop()
	}
}

// RemoveSession deletes a miner session from tracking map.
func (s *StratumServer) RemoveSession(id string) {
	s.sessMu.Lock()
	delete(s.sessions, id)
	s.sessMu.Unlock()
}

// BroadcastJob pushes a new work template to all connected miners.
func (s *StratumServer) BroadcastJob(job *StratumJob) {
	s.sessMu.RLock()
	defer s.sessMu.RUnlock()

	for _, sess := range s.sessions {
		go func(session *Session) {
			if err := session.PushJob(job); err != nil {
				log.Printf("[Stratum] Failed to push job to %s: %v", session.id, err)
			}
		}(sess)
	}
}

// pollWork queries core-rogue for current work package.
func (s *StratumServer) pollWork() error {
	wp, err := s.rpc.GetWork()
	if err != nil {
		return err
	}

	cleanSeal := CleanHex(wp.SealHash)
	if cleanSeal == s.lastSealHash {
		return nil // Work has not changed
	}

	cleanJobs := (s.lastSealHash != "")
	s.lastSealHash = cleanSeal

	job, err := s.jobs.CreateJob(wp, cleanJobs)
	if err != nil {
		return fmt.Errorf("failed to create stratum job: %w", err)
	}

	log.Printf("[Stratum] New block template: height=%d, sealhash=0x%s, seed=0x%s... (clean_jobs=%v)",
		job.Height, job.SealHashHex(), job.SeedHash[:16], cleanJobs)

	// Distribute to all connected miners
	s.BroadcastJob(job)
	return nil
}

func (s *StratumServer) workLoop(ctx context.Context) {
	ticker := time.NewTicker(s.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			if err := s.pollWork(); err != nil {
				// Avoid log spam on transient network hiccups
				if s.config.LogLevel == "debug" {
					log.Printf("[Stratum] pollWork error: %v", err)
				}
			}
		}
	}
}

// SealHashHex returns 64-hex-char seal hash representation for logging.
func (j *StratumJob) SealHashHex() string {
	return hex.EncodeToString(j.SealHash[:])
}

// SubmitBlock constructs the full 64-bit nonce and submits the solved block to Core-Rogue.
func (s *StratumServer) SubmitBlock(job *StratumJob, minerNonceHex, resultHex string) {
	minerNonce, err := ParseMinerNonce(minerNonceHex)
	if err != nil {
		log.Printf("[Stratum] Failed to parse miner nonce %s: %v", minerNonceHex, err)
		return
	}

	// Combine upper 32-bit ExtraNonce and miner nonce into 64-bit BlockNonce
	fullNonce := CalculateFullNonce(job.ExtraNonce, uint32(minerNonce))
	nonceFormatted := fmt.Sprintf("0x%016x", fullNonce)
	sealHashFormatted := "0x" + hex.EncodeToString(job.SealHash[:])
	digestFormatted := "0x" + CleanHex(resultHex)

	log.Printf("[Stratum] Broadcasting block to node: nonce=%s, seal=%s, digest=%s",
		nonceFormatted, sealHashFormatted, digestFormatted)

	accepted, err := s.rpc.SubmitWork(nonceFormatted, sealHashFormatted, digestFormatted)
	if err != nil {
		log.Printf("❌ [Stratum] SubmitWork RPC call failed: %v", err)
		return
	}

	if accepted {
		log.Printf("✅ [Stratum] BLOCK ACCEPTED BY ROGUE SIGNAL NETWORK! Height: %d", job.Height)
		// Immediately poll for the next block work
		_ = s.pollWork()
	} else {
		log.Printf("⚠️ [Stratum] Block was rejected by node (stale or invalid PoW)")
	}
}
