package store

import (
	"testing"

	"github.com/waywardgeek/gateway/pkg/types"
)

func TestEnqueueAndAck(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New store: %v", err)
	}

	// Enqueue 3 messages
	for i := 0; i < 3; i++ {
		st.EnqueuePrompt("agent-1", types.PromptEnvelope{
			MessageID: "msg-" + string(rune('a'+i)),
			Content:   "hello",
		})
	}

	// Should have 3 pending
	pending := st.GetPendingPrompts("agent-1", 0)
	if len(pending) != 3 {
		t.Fatalf("expected 3 pending, got %d", len(pending))
	}

	// Seq should be 1, 2, 3
	for i, p := range pending {
		expected := int64(i + 1)
		if p.Seq != expected {
			t.Errorf("expected seq %d, got %d", expected, p.Seq)
		}
	}

	// Ack seq 2 — should remove msgs 1 and 2
	st.AckPrompt("agent-1", 2)
	pending = st.GetPendingPrompts("agent-1", 0)
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending after ack, got %d", len(pending))
	}
	if pending[0].Seq != 3 {
		t.Errorf("expected remaining seq 3, got %d", pending[0].Seq)
	}
}

func TestGetPendingAfterSeq(t *testing.T) {
	dir := t.TempDir()
	st, _ := New(dir)

	for i := 0; i < 5; i++ {
		st.EnqueuePrompt("agent-1", types.PromptEnvelope{Content: "hello"})
	}

	// Get pending after seq 3
	pending := st.GetPendingPrompts("agent-1", 3)
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending after seq 3, got %d", len(pending))
	}
	if pending[0].Seq != 4 || pending[1].Seq != 5 {
		t.Errorf("unexpected seqs: %d, %d", pending[0].Seq, pending[1].Seq)
	}
}

func TestSaveAndReload(t *testing.T) {
	dir := t.TempDir()
	st, _ := New(dir)

	st.EnqueuePrompt("agent-1", types.PromptEnvelope{MessageID: "test", Content: "hello"})

	// Save to disk
	if err := st.SaveAll(); err != nil {
		t.Fatalf("SaveAll: %v", err)
	}

	// Reload from disk
	st2, err := New(dir)
	if err != nil {
		t.Fatalf("reload store: %v", err)
	}

	pending := st2.GetPendingPrompts("agent-1", 0)
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending after reload, got %d", len(pending))
	}
	if pending[0].Content != "hello" {
		t.Errorf("expected 'hello', got %q", pending[0].Content)
	}
}

func TestJobCRUD(t *testing.T) {
	dir := t.TempDir()
	st, _ := New(dir)

	job := &types.Job{
		ID:         "job-1",
		Name:       "test-job",
		OwnerAgent: "agent-1",
		Source:     "dynamic",
		Prompt:     "do the thing",
	}

	st.SaveJob(job)

	// List by agent
	jobs := st.GetAgentJobs("agent-1")
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].Name != "test-job" {
		t.Errorf("expected 'test-job', got %q", jobs[0].Name)
	}

	// Delete
	if !st.DeleteJob("job-1") {
		t.Error("expected DeleteJob to return true")
	}
	if st.DeleteJob("job-1") {
		t.Error("expected DeleteJob to return false on second call")
	}

	// Save and reload dynamic jobs
	st.SaveJob(&types.Job{ID: "job-2", OwnerAgent: "agent-1", Source: "dynamic"})
	st.SaveJobs()

	st2, _ := New(dir)
	if len(st2.GetDynamicJobs()) != 1 {
		t.Errorf("expected 1 dynamic job after reload, got %d", len(st2.GetDynamicJobs()))
	}
}
