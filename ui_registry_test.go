package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAgentTokenRegistry(t *testing.T) {
	registry := newAgentTokenRegistry(time.Minute, 10)
	agent := agentInfo{PaneID: "w1:p1", TerminalID: "term-1", Name: "reviewer"}
	token, ok := registry.register(agent)
	if !ok || len(token) != 24 {
		t.Fatalf("register token = %q, ok=%v", token, ok)
	}
	got, ok := registry.get(token, false)
	if !ok || got.TerminalID != agent.TerminalID {
		t.Fatalf("resolved agent = %+v, ok=%v", got, ok)
	}
	if _, ok := registry.get(token, true); !ok {
		t.Fatal("failed to consume token")
	}
	if _, ok := registry.get(token, false); ok {
		t.Fatal("consumed token remained valid")
	}
}

func TestPromptRegistryClaimsReplyOnce(t *testing.T) {
	registry := newPromptRegistry(time.Minute, 10)
	agent := agentInfo{PaneID: "w1:p1", TerminalID: "term-1"}
	registry.register(42, 7, 42, agent)
	if _, ok := registry.claim(42, 7, 7); ok {
		t.Fatal("wrong owner claimed prompt")
	}
	registry.register(42, 8, 42, agent)
	if _, ok := registry.claim(42, 8, 42); !ok {
		t.Fatal("owner failed to claim prompt")
	}
	if _, ok := registry.claim(42, 8, 42); ok {
		t.Fatal("prompt was claimed twice")
	}
}

func TestPromptRegistryConcurrentClaim(t *testing.T) {
	registry := newPromptRegistry(time.Minute, 10)
	registry.register(42, 7, 42, agentInfo{PaneID: "w1:p1", TerminalID: "term-1"})
	var claims int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := registry.claim(42, 7, 42); ok {
				atomic.AddInt32(&claims, 1)
			}
		}()
	}
	wg.Wait()
	if claims != 1 {
		t.Fatalf("prompt claimed %d times, want once", claims)
	}
}

func TestAgentTokenRegistryEvictsOldest(t *testing.T) {
	registry := newAgentTokenRegistry(time.Minute, 2)
	first, _ := registry.register(agentInfo{PaneID: "w1:p1", TerminalID: "term-1"})
	time.Sleep(time.Millisecond)
	second, _ := registry.register(agentInfo{PaneID: "w1:p2", TerminalID: "term-2"})
	time.Sleep(time.Millisecond)
	third, _ := registry.register(agentInfo{PaneID: "w1:p3", TerminalID: "term-3"})
	if _, ok := registry.get(first, false); ok {
		t.Fatal("oldest token was not evicted")
	}
	if _, ok := registry.get(second, false); !ok {
		t.Fatal("second token was unexpectedly evicted")
	}
	if _, ok := registry.get(third, false); !ok {
		t.Fatal("newest token was unexpectedly evicted")
	}
}

func TestBlockedResponseRegistryIsOneUse(t *testing.T) {
	registry := newBlockedResponseRegistry(time.Minute)
	agent := agentInfo{PaneID: "w1:p1", TerminalID: "term-1", Agent: "claude", StateChangeSeq: 10}
	token, ok := registry.register(agent, "Approve", "fingerprint", "group-1")
	if !ok {
		t.Fatal("failed to register blocked response")
	}
	sibling, ok := registry.register(agent, "Reject", "fingerprint", "group-1")
	if !ok {
		t.Fatal("failed to register sibling blocked response")
	}
	response, ok := registry.claim(token)
	if !ok || response.Text != "Approve" || response.Fingerprint != "fingerprint" {
		t.Fatalf("unexpected response: %+v, ok=%v", response, ok)
	}
	if _, ok := registry.claim(token); ok {
		t.Fatal("blocked response was claimed twice")
	}
	if _, ok := registry.claim(sibling); ok {
		t.Fatal("sibling response remained valid after a selection")
	}
}
