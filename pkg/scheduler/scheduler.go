// Package scheduler provides the built-in cron-like scheduler.
// Two sources: static jobs from config + dynamic jobs from agents.
// Fires prompts into the router — same path as any channel-originated prompt.
package scheduler

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/waywardgeek/gateway/pkg/config"
	"github.com/waywardgeek/gateway/pkg/router"
	"github.com/waywardgeek/gateway/pkg/store"
	"github.com/waywardgeek/gateway/pkg/types"
)

// Scheduler manages scheduled jobs and fires them into the router.
type Scheduler struct {
	mu      sync.Mutex
	cfg     *config.Config
	router  *router.Router
	store   *store.Store
	cancels map[string]context.CancelFunc // job ID → cancel
	ctx     context.Context
	cancel  context.CancelFunc
}

// New creates a new scheduler.
func New(cfg *config.Config, r *router.Router, st *store.Store) *Scheduler {
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		cfg:     cfg,
		router:  r,
		store:   st,
		cancels: make(map[string]context.CancelFunc),
		ctx:     ctx,
		cancel:  cancel,
	}
}

// Start loads all jobs and starts their goroutines.
func (s *Scheduler) Start() {
	// Load static jobs from config
	for name, jobCfg := range s.cfg.Scheduler.Jobs {
		job := types.Job{
			ID:              "static-" + name,
			Name:            name,
			OwnerAgent:      "gateway",
			Source:          "static",
			Schedule:        types.JobSchedule{Type: "cron", Cron: jobCfg.Schedule},
			Prompt:          jobCfg.Prompt,
			RouteTo:         jobCfg.RouteTo,
			ResponseChannel: jobCfg.ResponseChannel,
			CreatedAt:       time.Now().UTC(),
		}
		s.startJob(job)
	}

	// Load dynamic jobs from store
	for _, job := range s.store.GetDynamicJobs() {
		s.startJob(*job)
	}

	log.Printf("[scheduler] started with %d static + %d dynamic jobs",
		len(s.cfg.Scheduler.Jobs), len(s.store.GetDynamicJobs()))
}

// Stop cancels all running job goroutines.
func (s *Scheduler) Stop() {
	s.cancel()
}

// CreateJob creates a new dynamic job and starts it.
func (s *Scheduler) CreateJob(ownerAgent, name, cron, onceAt, prompt, responseChannel string) (*types.Job, error) {
	job := types.Job{
		ID:              uuid.Must(uuid.NewV7()).String(),
		Name:            name,
		OwnerAgent:      ownerAgent,
		Source:          "dynamic",
		Prompt:          prompt,
		RouteTo:         ownerAgent, // dynamic jobs route to the creating agent
		ResponseChannel: responseChannel,
		CreatedAt:       time.Now().UTC(),
	}

	if cron != "" {
		job.Schedule = types.JobSchedule{Type: "cron", Cron: cron}
	} else if onceAt != "" {
		t, err := time.Parse(time.RFC3339, onceAt)
		if err != nil {
			return nil, fmt.Errorf("invalid once_at time: %w", err)
		}
		if t.Before(time.Now()) {
			return nil, fmt.Errorf("once_at time is in the past")
		}
		job.Schedule = types.JobSchedule{Type: "once", OnceAt: t}
	} else {
		return nil, fmt.Errorf("either cron or once_at is required")
	}

	s.store.SaveJob(&job)
	s.startJob(job)

	log.Printf("[scheduler] created dynamic job %s (%s) for agent %s", job.ID, name, ownerAgent)
	return &job, nil
}

// DeleteJob stops and removes a dynamic job.
func (s *Scheduler) DeleteJob(ownerAgent, jobID string) error {
	// Check ownership via store
	jobs := s.store.GetAgentJobs(ownerAgent)
	found := false
	for _, j := range jobs {
		if j.ID == jobID {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("job %s not found or not owned by %s", jobID, ownerAgent)
	}

	s.mu.Lock()
	if cancel, ok := s.cancels[jobID]; ok {
		cancel()
		delete(s.cancels, jobID)
	}
	s.mu.Unlock()

	s.store.DeleteJob(jobID)
	log.Printf("[scheduler] deleted job %s", jobID)
	return nil
}

// ListJobs returns all dynamic jobs owned by the given agent.
func (s *Scheduler) ListJobs(ownerAgent string) []*types.Job {
	return s.store.GetAgentJobs(ownerAgent)
}

// startJob launches a goroutine for a job.
func (s *Scheduler) startJob(job types.Job) {
	ctx, cancel := context.WithCancel(s.ctx)
	s.mu.Lock()
	s.cancels[job.ID] = cancel
	s.mu.Unlock()

	go s.runJob(ctx, job)
}

// runJob is the goroutine loop for a single job.
func (s *Scheduler) runJob(ctx context.Context, job types.Job) {
	for {
		next, err := nextFireTime(job.Schedule, time.Now())
		if err != nil {
			log.Printf("[scheduler] job %s (%s): bad schedule: %v", job.ID, job.Name, err)
			return
		}

		delay := time.Until(next)
		if delay < 0 {
			delay = 0
		}

		select {
		case <-time.After(delay):
			s.fireJob(job)
			if job.Schedule.Type == "once" {
				s.mu.Lock()
				delete(s.cancels, job.ID)
				s.mu.Unlock()
				s.store.DeleteJob(job.ID)
				log.Printf("[scheduler] one-shot job %s (%s) fired and removed", job.ID, job.Name)
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// fireJob creates a prompt envelope and delivers it via the router.
func (s *Scheduler) fireJob(job types.Job) {
	env := types.PromptEnvelope{
		AgentID: job.RouteTo,
		Source: types.PromptSource{
			Type:      "scheduler",
			ChannelID: "scheduler",
			UserID:    job.Name,
			Trust:     types.TrustOwner,
		},
		Content:      job.Prompt,
		ResponseMode: types.ResponseFireAndForget,
	}
	if job.ResponseChannel != "" {
		env.ResponseMode = types.ResponseAsync
		env.Metadata = map[string]string{"response_channel": job.ResponseChannel}
	}

	s.router.DeliverEnvelope(env)
	log.Printf("[scheduler] fired job %s (%s) → agent %s", job.ID, job.Name, job.RouteTo)
}

// nextFireTime calculates when a job should next fire.
func nextFireTime(sched types.JobSchedule, now time.Time) (time.Time, error) {
	switch sched.Type {
	case "once":
		return sched.OnceAt, nil
	case "cron":
		return parseCronNext(sched.Cron, now)
	default:
		return time.Time{}, fmt.Errorf("unknown schedule type: %s", sched.Type)
	}
}

// parseCronNext is a simple cron parser supporting "min hour dom month dow" format.
// For MVP, we support only fixed times: "30 9 * * *" means 9:30 AM daily.
func parseCronNext(expr string, now time.Time) (time.Time, error) {
	var min, hour int
	var domStr, monStr, dowStr string
	_, err := fmt.Sscanf(expr, "%d %d %s %s %s", &min, &hour, &domStr, &monStr, &dowStr)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse cron %q: %w (supported: 'min hour * * *')", expr, err)
	}

	// Calculate next occurrence
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, now.Location())
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return next, nil
}
