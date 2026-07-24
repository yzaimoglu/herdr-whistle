package main

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

type agentTokenRegistry struct {
	mu      sync.Mutex
	entries map[string]agentTokenEntry
	ttl     time.Duration
	max     int
}

type agentTokenEntry struct {
	Agent     agentInfo
	ExpiresAt time.Time
}

func newAgentTokenRegistry(ttl time.Duration, max int) *agentTokenRegistry {
	return &agentTokenRegistry{entries: make(map[string]agentTokenEntry), ttl: ttl, max: max}
}

var agentUITokens = newAgentTokenRegistry(30*time.Minute, 256)
var confirmationTokens = newAgentTokenRegistry(5*time.Minute, 128)

func randomUIToken() (string, bool) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", false
	}
	return hex.EncodeToString(raw[:]), true
}

func (r *agentTokenRegistry) register(agent agentInfo) (string, bool) {
	if agent.PaneID == "" || agent.TerminalID == "" {
		return "", false
	}
	token, ok := randomUIToken()
	if !ok {
		return "", false
	}
	now := time.Now()

	r.mu.Lock()
	defer r.mu.Unlock()
	for key, entry := range r.entries {
		if now.After(entry.ExpiresAt) {
			delete(r.entries, key)
		}
	}
	if r.max > 0 && len(r.entries) >= r.max {
		var oldestKey string
		var oldestExpiry time.Time
		for key, entry := range r.entries {
			if oldestKey == "" || entry.ExpiresAt.Before(oldestExpiry) {
				oldestKey = key
				oldestExpiry = entry.ExpiresAt
			}
		}
		delete(r.entries, oldestKey)
	}
	r.entries[token] = agentTokenEntry{Agent: agent, ExpiresAt: now.Add(r.ttl)}
	return token, true
}

func (r *agentTokenRegistry) get(token string, consume bool) (agentInfo, bool) {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[token]
	if !ok {
		return agentInfo{}, false
	}
	if consume {
		delete(r.entries, token)
	}
	if now.After(entry.ExpiresAt) {
		delete(r.entries, token)
		return agentInfo{}, false
	}
	return entry.Agent, true
}

type pendingPromptKey struct {
	ChatID    int64
	MessageID int
}

type pendingPrompt struct {
	OwnerID   int64
	Agent     agentInfo
	ExpiresAt time.Time
}

type promptRegistry struct {
	mu      sync.Mutex
	entries map[pendingPromptKey]pendingPrompt
	ttl     time.Duration
	max     int
}

func newPromptRegistry(ttl time.Duration, max int) *promptRegistry {
	return &promptRegistry{entries: make(map[pendingPromptKey]pendingPrompt), ttl: ttl, max: max}
}

var pendingPrompts = newPromptRegistry(10*time.Minute, 64)

func (r *promptRegistry) register(chatID int64, messageID int, ownerID int64, agent agentInfo) {
	now := time.Now()
	key := pendingPromptKey{ChatID: chatID, MessageID: messageID}
	r.mu.Lock()
	defer r.mu.Unlock()
	for existingKey, entry := range r.entries {
		if now.After(entry.ExpiresAt) {
			delete(r.entries, existingKey)
		}
	}
	if r.max > 0 && len(r.entries) >= r.max {
		var oldestKey pendingPromptKey
		var oldestExpiry time.Time
		first := true
		for existingKey, entry := range r.entries {
			if first || entry.ExpiresAt.Before(oldestExpiry) {
				oldestKey = existingKey
				oldestExpiry = entry.ExpiresAt
				first = false
			}
		}
		delete(r.entries, oldestKey)
	}
	r.entries[key] = pendingPrompt{OwnerID: ownerID, Agent: agent, ExpiresAt: now.Add(r.ttl)}
}

func (r *promptRegistry) claim(chatID int64, messageID int, ownerID int64) (agentInfo, bool) {
	key := pendingPromptKey{ChatID: chatID, MessageID: messageID}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[key]
	if !ok {
		return agentInfo{}, false
	}
	if now.After(entry.ExpiresAt) {
		delete(r.entries, key)
		return agentInfo{}, false
	}
	if entry.OwnerID != ownerID {
		return agentInfo{}, false
	}
	delete(r.entries, key)
	return entry.Agent, true
}

func (r *promptRegistry) has(chatID int64, messageID int, ownerID int64) bool {
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

type blockedResponse struct {
	Agent       agentInfo
	Text        string
	Fingerprint string
	Group       string
	ExpiresAt   time.Time
}

type blockedResponseRegistry struct {
	mu      sync.Mutex
	entries map[string]blockedResponse
	ttl     time.Duration
}

func newBlockedResponseRegistry(ttl time.Duration) *blockedResponseRegistry {
	return &blockedResponseRegistry{entries: make(map[string]blockedResponse), ttl: ttl}
}

var pendingBlockedResponses = newBlockedResponseRegistry(10 * time.Minute)

func (r *blockedResponseRegistry) register(agent agentInfo, text, fingerprint, group string) (string, bool) {
	if agent.PaneID == "" || agent.TerminalID == "" || agent.StateChangeSeq == 0 || strings.TrimSpace(text) == "" || fingerprint == "" || group == "" {
		return "", false
	}
	token, ok := randomUIToken()
	if !ok {
		return "", false
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, entry := range r.entries {
		if now.After(entry.ExpiresAt) {
			delete(r.entries, key)
		}
	}
	r.entries[token] = blockedResponse{Agent: agent, Text: text, Fingerprint: fingerprint, Group: group, ExpiresAt: now.Add(r.ttl)}
	return token, true
}

func (r *blockedResponseRegistry) claim(token string) (blockedResponse, bool) {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[token]
	if !ok {
		return blockedResponse{}, false
	}
	for key, candidate := range r.entries {
		if candidate.Group == entry.Group {
			delete(r.entries, key)
		}
	}
	if now.After(entry.ExpiresAt) {
		return blockedResponse{}, false
	}
	return entry, true
}
