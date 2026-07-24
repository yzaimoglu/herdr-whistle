package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type userPreferences struct {
	NotifyBlocked   bool   `json:"notify_blocked"`
	NotifyCompleted bool   `json:"notify_completed"`
	NotifyStarted   bool   `json:"notify_started"`
	UIMode          string `json:"ui_mode"`
}

type persistedUXState struct {
	Version     int             `json:"version"`
	Preferences userPreferences `json:"preferences"`
}

type uxStateStore struct {
	mu    sync.Mutex
	path  string
	state persistedUXState
}

var uxState = uxStateStore{state: defaultUXState()}

func defaultUXState() persistedUXState {
	return persistedUXState{
		Version: 1,
		Preferences: userPreferences{
			NotifyBlocked: true, NotifyCompleted: true, NotifyStarted: true, UIMode: "dashboard",
		},
	}
}

func initUXState(dir string) error {
	path := filepath.Join(dir, "ux-state.json")
	state := defaultUXState()
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &state); err != nil {
			state = defaultUXState()
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if state.Preferences.UIMode != "compact" {
		state.Preferences.UIMode = "dashboard"
	}
	uxState.mu.Lock()
	uxState.path = path
	uxState.state = state
	uxState.mu.Unlock()
	return nil
}

func currentPreferences() userPreferences {
	uxState.mu.Lock()
	defer uxState.mu.Unlock()
	return uxState.state.Preferences
}

func updatePreferences(update func(*userPreferences)) error {
	uxState.mu.Lock()
	defer uxState.mu.Unlock()
	update(&uxState.state.Preferences)
	return uxState.saveLocked()
}

func (s *uxStateStore) saveLocked() error {
	if s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(append(data, '\n')); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

type activityEvent struct {
	At      time.Time
	Kind    string
	Agent   string
	PaneID  string
	Summary string
}

type activityBuffer struct {
	mu     sync.Mutex
	events []activityEvent
	max    int
}

var recentActivity = activityBuffer{max: 80}

func recordActivity(kind string, agent agentInfo, summary string) {
	event := activityEvent{At: time.Now(), Kind: kind, Agent: agentUILabel(agent), PaneID: agent.PaneID, Summary: summary}
	recentActivity.mu.Lock()
	defer recentActivity.mu.Unlock()
	recentActivity.events = append(recentActivity.events, event)
	if len(recentActivity.events) > recentActivity.max {
		recentActivity.events = recentActivity.events[len(recentActivity.events)-recentActivity.max:]
	}
}

func activityPage(page, size int) ([]activityEvent, int) {
	recentActivity.mu.Lock()
	defer recentActivity.mu.Unlock()
	if size <= 0 {
		size = 10
	}
	totalPages := (len(recentActivity.events) + size - 1) / size
	if totalPages == 0 {
		totalPages = 1
	}
	if page < 0 {
		page = 0
	}
	if page >= totalPages {
		page = totalPages - 1
	}
	start := len(recentActivity.events) - page*size
	end := start
	start -= size
	if start < 0 {
		start = 0
	}
	result := make([]activityEvent, 0, end-start)
	for i := end - 1; i >= start; i-- {
		result = append(result, recentActivity.events[i])
	}
	return result, totalPages
}

type blockedMessageRef struct {
	ChatID    int64
	MessageID int
	Agent     agentInfo
}

var blockedMessages = struct {
	sync.Mutex
	items map[string]blockedMessageRef
}{items: make(map[string]blockedMessageRef)}

func rememberBlockedMessage(agent agentInfo, chatID int64, messageID int) {
	blockedMessages.Lock()
	blockedMessages.items[agent.TerminalID] = blockedMessageRef{ChatID: chatID, MessageID: messageID, Agent: agent}
	blockedMessages.Unlock()
}

func takeBlockedMessage(terminalID string) (blockedMessageRef, bool) {
	blockedMessages.Lock()
	defer blockedMessages.Unlock()
	ref, ok := blockedMessages.items[terminalID]
	if ok {
		delete(blockedMessages.items, terminalID)
	}
	return ref, ok
}

func formatActivityTime(at time.Time) string {
	return at.Local().Format("15:04")
}

func activitySummary(event activityEvent) string {
	return fmt.Sprintf("<code>%s</code> %s <b>%s</b> · %s", formatActivityTime(event.At), escapeHTML(event.Kind), escapeHTML(event.Agent), escapeHTML(event.Summary))
}
