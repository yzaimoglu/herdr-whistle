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
	MenuHash    string
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

type pendingCustomChoice struct {
	OwnerID   int64
	Choice    pendingChoice
	Index     int
	ExpiresAt time.Time
}

type customChoiceRegistry struct {
	mu      sync.Mutex
	entries map[pendingPromptKey]pendingCustomChoice
	ttl     time.Duration
}

func newCustomChoiceRegistry(ttl time.Duration) *customChoiceRegistry {
	return &customChoiceRegistry{entries: make(map[pendingPromptKey]pendingCustomChoice), ttl: ttl}
}

var pendingCustomChoices = newCustomChoiceRegistry(10 * time.Minute)

func (r *customChoiceRegistry) register(chatID int64, messageID int, ownerID int64, choice pendingChoice, index int) {
	now := time.Now()
	key := pendingPromptKey{ChatID: chatID, MessageID: messageID}
	r.mu.Lock()
	defer r.mu.Unlock()
	for existingKey, entry := range r.entries {
		if now.After(entry.ExpiresAt) {
			delete(r.entries, existingKey)
		}
	}
	r.entries[key] = pendingCustomChoice{OwnerID: ownerID, Choice: choice, Index: index, ExpiresAt: now.Add(r.ttl)}
}

func (r *customChoiceRegistry) has(chatID int64, messageID int, ownerID int64) bool {
	key := pendingPromptKey{ChatID: chatID, MessageID: messageID}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[key]
	if !ok {
		return false
	}
	if now.After(entry.ExpiresAt) {
		delete(r.entries, key)
		return false
	}
	return entry.OwnerID == ownerID
}

func (r *customChoiceRegistry) claim(chatID int64, messageID int, ownerID int64) (pendingCustomChoice, bool) {
	key := pendingPromptKey{ChatID: chatID, MessageID: messageID}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[key]
	if !ok || now.After(entry.ExpiresAt) || entry.OwnerID != ownerID {
		if ok && now.After(entry.ExpiresAt) {
			delete(r.entries, key)
		}
		return pendingCustomChoice{}, false
	}
	delete(r.entries, key)
	return entry, true
}

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
		value.WriteByte(0)
		value.WriteString(choice.NavigateKey)
		if choice.Custom {
			value.WriteByte(1)
		}
		if choice.Skip {
			value.WriteByte(2)
		}
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
		MenuHash:    choiceMenuFingerprint(pc),
		ChoiceCount: len(pc.Choices),
		ExpiresAt:   now.Add(r.ttl),
	}
	return nonce, true
}

func matchesPendingChoice(pending pendingChoice, current *parsedChoices) bool {
	if current == nil || current.MultiSelect {
		return false
	}
	return choiceMenuFingerprint(current) == pending.MenuHash
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
