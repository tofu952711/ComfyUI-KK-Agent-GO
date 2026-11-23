package agent

import (
	"context"
	"fmt"
	"kkagent/internal/model"
	"strings"
	"sync"
	"testing"
	"time"
)

type sessionProbe struct {
	mu        sync.Mutex
	counts    map[string]int
	histories map[string]string
}

func (p *sessionProbe) Next(_ context.Context, r model.Request, _ model.Delta) (model.Turn, error) {
	last := r.Messages[len(r.Messages)-1].Content[0].Text
	var text strings.Builder
	for _, message := range r.Messages {
		for _, block := range message.Content {
			if block.Type == "text" {
				text.WriteString(block.Text)
				text.WriteByte('\n')
			}
		}
	}
	p.mu.Lock()
	p.counts[last] = len(r.Messages)
	p.histories[last] = text.String()
	p.mu.Unlock()
	return model.Turn{Message: model.Message{Role: "assistant", Content: []model.Block{{Type: "text", Text: "answer:" + last}}},
		Finish: "completed", Usage: model.Usage{Known: true}, Elapsed: time.Millisecond}, nil
}

func TestWorkflowNamespaceIsolatesHistoryAndRetainsWithinWorkflowTurns(t *testing.T) {
	p := &sessionProbe{counts: map[string]int{}, histories: map[string]string{}}
	e := newEngine(t, p, &fakeTools{})
	run := func(id, namespace, task string) {
		s := spec(id)
		s.SessionID, s.SessionNamespace, s.Task = "same-logical-stage", namespace, task
		snapshot, err := e.Start(s)
		if err != nil {
			t.Fatal(err)
		}
		if done := wait(t, e, snapshot.ID); done.Status != "completed" {
			t.Fatalf("failed: %+v", done)
		}
	}
	// Legacy stale histories must remain stored but inaccessible to a new workflow.
	run("legacy", "", "legacy-sentinel")
	run("A1", "workflow-A", "A-first")
	run("B1", "workflow-B", "B-first")
	run("A2", "workflow-A", "A-followup")
	if p.counts["A-first"] != 1 || p.counts["B-first"] != 1 || p.counts["A-followup"] != 3 {
		t.Fatalf("wrong session isolation: %v", p.counts)
	}
	if strings.Contains(p.histories["A-first"], "legacy-sentinel") || strings.Contains(p.histories["B-first"], "A-first") || strings.Contains(p.histories["A-followup"], "B-first") {
		t.Fatal("cross-workflow history leaked")
	}
	if !strings.Contains(p.histories["A-followup"], "answer:A-first") {
		t.Fatal("lost same-workflow history")
	}
}

func TestConcurrentWorkflowNamespacesRemainIsolatedAndIdempotent(t *testing.T) {
	p := &sessionProbe{counts: map[string]int{}, histories: map[string]string{}}
	e := newEngine(t, p, &fakeTools{})
	snapshots := []model.Snapshot{}
	for i := 0; i < 4; i++ {
		s := spec(fmt.Sprintf("parallel-%d", i))
		s.SessionID, s.SessionNamespace = "same-stage", fmt.Sprintf("workflow-%d", i)
		first, err := e.Start(s)
		if err != nil {
			t.Fatal(err)
		}
		duplicate, err := e.Start(s)
		if err != nil || first.ID != duplicate.ID {
			t.Fatal("request retry lost idempotency")
		}
		snapshots = append(snapshots, first)
	}
	for _, snapshot := range snapshots {
		if done := wait(t, e, snapshot.ID); done.Status != "completed" {
			t.Fatal(done)
		}
	}
	for task, count := range p.counts {
		if count != 1 {
			t.Fatalf("history leaked into %s: %d messages", task, count)
		}
	}
	s := spec("oversized-namespace")
	s.SessionNamespace = strings.Repeat("x", 257)
	if _, err := e.Start(s); err == nil {
		t.Fatal("unbounded namespace accepted")
	}
}
