package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

type pendingChoice struct {
	PaneID      string
	TerminalID  string
	AgentName   string
	AgentKind   string
	SessionID   string
	StateSeq    uint64
	Fingerprint string
	ChoiceCount int
	ExpiresAt   time.Time
}

type choiceRegistry struct {
	mu      sync.Mutex
	entries map[string]pendingChoice
	ttl     time.Duration
}

func newChoiceRegistry(ttl time.Duration) *choiceRegistry {
	return &choiceRegistry{entries: make(map[string]pendingChoice), ttl: ttl}
}

var pendingChoices = newChoiceRegistry(10 * time.Minute)

func choiceFingerprint(pc *parsedChoices) string {
	var value strings.Builder
	value.WriteString(pc.SourceFingerprint)
	value.WriteByte(0)
	value.WriteString(choiceMenuFingerprint(pc))
	sum := sha256.Sum256([]byte(value.String()))
	return hex.EncodeToString(sum[:])
}

func choiceMenuFingerprint(pc *parsedChoices) string {
	var value strings.Builder
	value.WriteString(pc.Prompt)
	for _, choice := range pc.Choices {
		value.WriteByte(0)
		value.WriteString(choice.CleanText)
		value.WriteByte(0)
		value.WriteString(choice.DirectKey)
	}
	sum := sha256.Sum256([]byte(value.String()))
	return hex.EncodeToString(sum[:])
}

func (r *choiceRegistry) register(agent agentInfo, pc *parsedChoices) (string, bool) {
	if agent.PaneID == "" || agent.TerminalID == "" || agent.Agent == "" {
		return "", false
	}
	if agent.StateChangeSeq == 0 {
		return "", false
	}
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", false
	}
	nonce := hex.EncodeToString(raw[:])
	now := time.Now()

	r.mu.Lock()
	defer r.mu.Unlock()
	for key, entry := range r.entries {
		if now.After(entry.ExpiresAt) {
			delete(r.entries, key)
		}
	}
	r.entries[nonce] = pendingChoice{
		PaneID:      agent.PaneID,
		TerminalID:  agent.TerminalID,
		AgentName:   agent.Name,
		AgentKind:   agent.Agent,
		SessionID:   agent.AgentSession.Value,
		StateSeq:    agent.StateChangeSeq,
		Fingerprint: choiceFingerprint(pc),
		ChoiceCount: len(pc.Choices),
		ExpiresAt:   now.Add(r.ttl),
	}
	return nonce, true
}

// claim removes an entry before any pane operation so duplicate or concurrent
// taps can never submit the same approval twice.
func (r *choiceRegistry) claim(nonce string) (pendingChoice, bool) {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[nonce]
	if !ok {
		return pendingChoice{}, false
	}
	delete(r.entries, nonce)
	if now.After(entry.ExpiresAt) {
		return pendingChoice{}, false
	}
	return entry, true
}
