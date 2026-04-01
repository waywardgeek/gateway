// Package store provides JSON file persistence for gateway state.
// Same pattern as Haven — simple JSON files, git-backed.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/waywardgeek/gateway/pkg/types"
)

// Store manages persistent gateway state.
type Store struct {
	mu      sync.RWMutex
	dataDir string
	agents  map[string]*AgentState
	jobs    map[string]*types.Job
	// messageIndex maps message_id → PromptEnvelope for response routing.
	// Entries are cleared when acked.
	messageIndex map[string]*types.PromptEnvelope
}

// AgentState is the persistent state for a connected agent.
type AgentState struct {
	AgentID    string                `json:"agent_id"`
	LastSeen   string                `json:"last_seen,omitempty"`
	LastSeq    int64                 `json:"last_seq"`
	Queue      []types.PromptEnvelope `json:"queue"`
}

// New creates a new store backed by the given data directory.
func New(dataDir string) (*Store, error) {
	// Ensure directories exist
	for _, sub := range []string{"agents", "scheduler", "channels"} {
		if err := os.MkdirAll(filepath.Join(dataDir, sub), 0755); err != nil {
			return nil, fmt.Errorf("create %s dir: %w", sub, err)
		}
	}

	s := &Store{
		dataDir:      dataDir,
		agents:       make(map[string]*AgentState),
		jobs:         make(map[string]*types.Job),
		messageIndex: make(map[string]*types.PromptEnvelope),
	}

	if err := s.loadAgents(); err != nil {
		return nil, err
	}
	if err := s.loadJobs(); err != nil {
		return nil, err
	}

	return s, nil
}

// --- Agent State ---

func (s *Store) loadAgents() error {
	dir := filepath.Join(s.dataDir, "agents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil // empty dir is fine
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return fmt.Errorf("read agent state %s: %w", e.Name(), err)
		}
		var state AgentState
		if err := json.Unmarshal(data, &state); err != nil {
			return fmt.Errorf("parse agent state %s: %w", e.Name(), err)
		}
		s.agents[state.AgentID] = &state
	}
	return nil
}

// GetAgentState returns the stored state for an agent, creating it if needed.
func (s *Store) GetAgentState(agentID string) *AgentState {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.agents[agentID]
	if !ok {
		state = &AgentState{AgentID: agentID}
		s.agents[agentID] = state
	}
	return state
}

// EnqueuePrompt adds a prompt to an agent's delivery queue.
func (s *Store) EnqueuePrompt(agentID string, env types.PromptEnvelope) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.agents[agentID]
	if state == nil {
		state = &AgentState{AgentID: agentID}
		s.agents[agentID] = state
	}
	state.LastSeq++
	env.Seq = state.LastSeq
	state.Queue = append(state.Queue, env)
	// Index for response routing
	envCopy := env
	s.messageIndex[env.MessageID] = &envCopy
}

// AckPrompt removes all prompts up to and including seq from the queue.
func (s *Store) AckPrompt(agentID string, seq int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.agents[agentID]
	if state == nil {
		return
	}
	for i, env := range state.Queue {
		if env.Seq == seq {
			state.Queue = state.Queue[i+1:]
			return
		}
	}
}

// LookupMessage returns the original prompt envelope for a message ID.
// Used for response routing — find where the message came from.
func (s *Store) LookupMessage(messageID string) *types.PromptEnvelope {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.messageIndex[messageID]
}

// GetPendingPrompts returns all unacked prompts for an agent, optionally from a seq.
func (s *Store) GetPendingPrompts(agentID string, afterSeq int64) []types.PromptEnvelope {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := s.agents[agentID]
	if state == nil {
		return nil
	}
	var result []types.PromptEnvelope
	for _, env := range state.Queue {
		if env.Seq > afterSeq {
			result = append(result, env)
		}
	}
	return result
}

// SaveAgentState persists the agent state to disk.
func (s *Store) SaveAgentState(agentID string) error {
	s.mu.RLock()
	state := s.agents[agentID]
	s.mu.RUnlock()
	if state == nil {
		return nil
	}
	return s.writeJSON(filepath.Join("agents", agentID+".json"), state)
}

// --- Scheduler Jobs ---

func (s *Store) loadJobs() error {
	path := filepath.Join(s.dataDir, "scheduler", "dynamic-jobs.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read dynamic jobs: %w", err)
	}
	var jobs []*types.Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		return fmt.Errorf("parse dynamic jobs: %w", err)
	}
	for _, j := range jobs {
		s.jobs[j.ID] = j
	}
	return nil
}

// GetDynamicJobs returns all dynamic (agent-created) jobs.
func (s *Store) GetDynamicJobs() []*types.Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []*types.Job
	for _, j := range s.jobs {
		result = append(result, j)
	}
	return result
}

// GetAgentJobs returns all dynamic jobs owned by a specific agent.
func (s *Store) GetAgentJobs(agentID string) []*types.Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []*types.Job
	for _, j := range s.jobs {
		if j.OwnerAgent == agentID {
			result = append(result, j)
		}
	}
	return result
}

// SaveJob creates or updates a dynamic job.
func (s *Store) SaveJob(job *types.Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[job.ID] = job
}

// DeleteJob removes a dynamic job.
func (s *Store) DeleteJob(jobID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[jobID]; !ok {
		return false
	}
	delete(s.jobs, jobID)
	return true
}

// SaveJobs persists all dynamic jobs to disk.
func (s *Store) SaveJobs() error {
	s.mu.RLock()
	var jobs []*types.Job
	for _, j := range s.jobs {
		jobs = append(jobs, j)
	}
	s.mu.RUnlock()
	return s.writeJSON(filepath.Join("scheduler", "dynamic-jobs.json"), jobs)
}

// SaveAll persists all state to disk.
func (s *Store) SaveAll() error {
	s.mu.RLock()
	agentIDs := make([]string, 0, len(s.agents))
	for id := range s.agents {
		agentIDs = append(agentIDs, id)
	}
	s.mu.RUnlock()

	for _, id := range agentIDs {
		if err := s.SaveAgentState(id); err != nil {
			return fmt.Errorf("save agent %s: %w", id, err)
		}
	}
	return s.SaveJobs()
}

// writeJSON writes data as formatted JSON to a file.
func (s *Store) writeJSON(relPath string, v any) error {
	path := filepath.Join(s.dataDir, relPath)
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", relPath, err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write %s: %w", relPath, err)
	}
	return nil
}
