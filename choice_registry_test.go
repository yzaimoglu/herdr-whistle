package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestChoiceRegistryIsOneUse(t *testing.T) {
	registry := newChoiceRegistry(time.Minute)
	pc := &parsedChoices{Prompt: "Proceed?", ActiveIndex: 0, Choices: []parsedChoice{{CleanText: "Yes"}, {CleanText: "No"}}}
	agent := agentInfo{PaneID: "w1:p1", TerminalID: "terminal-1", Name: "reviewer", Agent: "claude", AgentSession: agentSession{Value: "session-1"}, StateChangeSeq: 10}
	nonce, ok := registry.register(agent, pc)
	if !ok || nonce == "" {
		t.Fatal("failed to register choice")
	}
	entry, ok := registry.claim(nonce)
	if !ok || entry.PaneID != "w1:p1" || entry.TerminalID != "terminal-1" || entry.AgentName != "reviewer" || entry.SessionID != "session-1" || entry.ChoiceCount != 2 {
		t.Fatalf("unexpected claimed entry: %+v, ok=%v", entry, ok)
	}
	if _, ok := registry.claim(nonce); ok {
		t.Fatal("choice nonce was accepted more than once")
	}
}

func TestChoiceRegistryRejectsExpiredChoice(t *testing.T) {
	registry := newChoiceRegistry(-time.Second)
	pc := &parsedChoices{Prompt: "Proceed?", ActiveIndex: 0, Choices: []parsedChoice{{CleanText: "Yes"}}}
	nonce, ok := registry.register(agentInfo{PaneID: "w1:p1", TerminalID: "terminal-1", Agent: "claude", AgentSession: agentSession{Value: "session-1"}, StateChangeSeq: 10}, pc)
	if !ok {
		t.Fatal("failed to register choice")
	}
	if _, ok := registry.claim(nonce); ok {
		t.Fatal("expired choice was accepted")
	}
}

func TestChoiceRegistryConcurrentClaim(t *testing.T) {
	registry := newChoiceRegistry(time.Minute)
	pc := &parsedChoices{Prompt: "Proceed?", ActiveIndex: 0, Choices: []parsedChoice{{CleanText: "Yes"}}}
	nonce, ok := registry.register(agentInfo{PaneID: "w1:p1", TerminalID: "terminal-1", Agent: "claude", AgentSession: agentSession{Value: "session-1"}, StateChangeSeq: 10}, pc)
	if !ok {
		t.Fatal("failed to register choice")
	}
	var accepted int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := registry.claim(nonce); ok {
				atomic.AddInt32(&accepted, 1)
			}
		}()
	}
	wg.Wait()
	if accepted != 1 {
		t.Fatalf("choice accepted %d times, want once", accepted)
	}
}

func TestChoiceFingerprintChangesWithPrompt(t *testing.T) {
	first := &parsedChoices{Prompt: "Proceed?", Choices: []parsedChoice{{CleanText: "Yes"}, {CleanText: "No"}}}
	second := &parsedChoices{Prompt: "Delete?", Choices: []parsedChoice{{CleanText: "Yes"}, {CleanText: "No"}}}
	if choiceFingerprint(first) == choiceFingerprint(second) {
		t.Fatal("different prompts produced the same fingerprint")
	}
}

func TestChoiceRegistryRequiresStableAgentIdentity(t *testing.T) {
	registry := newChoiceRegistry(time.Minute)
	pc := &parsedChoices{Prompt: "Proceed?", ActiveIndex: 0, Choices: []parsedChoice{{CleanText: "Yes"}}}
	if _, ok := registry.register(agentInfo{PaneID: "w1:p1", TerminalID: "terminal-1", Agent: "claude"}, pc); ok {
		t.Fatal("registered choice without a session or state generation")
	}
	if _, ok := registry.register(agentInfo{PaneID: "w1:p1", TerminalID: "terminal-1", Agent: "claude", StateChangeSeq: 12}, pc); !ok {
		t.Fatal("rejected sessionless agent with stable state generation")
	}
}
