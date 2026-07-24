package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot"
)

// sampleAgentListJSON simulates a herdr agent list response.
// result is a raw JSON object (not a string), matching the real herdr CLI format.
const sampleAgentListJSON = `{"id":"cli:agent:list","result":{"agents":[{"name":"reviewer","agent":"claude","agent_session":{"agent":"claude","kind":"id","source":"herdr:claude","value":"ses-111"},"agent_status":"%s","cwd":"/home/user/proj","focused":true,"pane_id":"wA:p1","tab_id":"wA:t1","terminal_id":"term_1","workspace_id":"wA"},{"name":"builder","agent":"opencode","agent_session":{"agent":"opencode","kind":"id","source":"herdr:opencode","value":"ses-222"},"agent_status":"%s","cwd":"/home/user/other","focused":false,"pane_id":"wB:p2","tab_id":"wB:t2","terminal_id":"term_2","workspace_id":"wB"}]}}`

func TestAgentWatcherParse(t *testing.T) {
	payload := fmt.Sprintf(sampleAgentListJSON, "idle", "idle")

	var env agentListEnvelope
	if err := json.Unmarshal([]byte(payload), &env); err != nil {
		t.Fatalf("failed to parse envelope: %v", err)
	}

	var lr agentListResult
	if err := json.Unmarshal(env.Result, &lr); err != nil {
		t.Fatalf("failed to parse result: %v", err)
	}

	if len(lr.Agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(lr.Agents))
	}

	if lr.Agents[0].Agent != "claude" {
		t.Errorf("expected agent[0].Agent='claude', got '%s'", lr.Agents[0].Agent)
	}
	if lr.Agents[0].AgentSession.Value != "ses-111" {
		t.Errorf("expected ses-111, got '%s'", lr.Agents[0].AgentSession.Value)
	}
	if lr.Agents[0].PaneID != "wA:p1" {
		t.Errorf("expected pane wA:p1, got '%s'", lr.Agents[0].PaneID)
	}
	if lr.Agents[0].AgentStatus != "idle" {
		t.Errorf("expected status idle, got '%s'", lr.Agents[0].AgentStatus)
	}
	if !lr.Agents[0].Focused {
		t.Error("expected agent[0] to be focused")
	}

	if lr.Agents[1].AgentStatus != "idle" {
		t.Errorf("expected agent[1] status idle, got '%s'", lr.Agents[1].AgentStatus)
	}
	if lr.Agents[1].Focused {
		t.Error("expected agent[1] to NOT be focused")
	}
}

func TestDetectTransitionToBlocked(t *testing.T) {
	// Simulate two poll cycles:
	// 1. Both agents start as "idle"
	// 2. Agent 0 transitions to "blocked"
	// Expect ONE notification.

	callCount := 0
	var lastAgent string
	callNum := 0

	herdrAgentList = func() (string, error) {
		callNum++
		switch callNum {
		case 1:
			return fmt.Sprintf(sampleAgentListJSON, "idle", "idle"), nil
		default:
			return fmt.Sprintf(sampleAgentListJSON, "blocked", "idle"), nil
		}
	}

	origNotify := notifyBlocked
	notifyBlocked = func(ctx context.Context, b *bot.Bot, chatID int64, a agentInfo) {
		callCount++
		lastAgent = a.Agent
		if a.PaneID != "wA:p1" {
			t.Errorf("expected pane wA:p1, got '%s'", a.PaneID)
		}
		if a.AgentStatus != "blocked" {
			t.Errorf("expected status blocked, got '%s'", a.AgentStatus)
		}
	}
	defer func() { notifyBlocked = origNotify }()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	agentWatcher(ctx, &bot.Bot{}, 12345)

	if callCount != 1 {
		t.Errorf("expected 1 notification, got %d (last agent: %s)", callCount, lastAgent)
	}
	if lastAgent != "claude" {
		t.Errorf("expected notification for claude, got '%s'", lastAgent)
	}
}

func TestNoNotificationOnSameStatus(t *testing.T) {
	callCount := 0
	herdrAgentList = func() (string, error) {
		return fmt.Sprintf(sampleAgentListJSON, "idle", "idle"), nil
	}

	origNotify := notifyBlocked
	notifyBlocked = func(ctx context.Context, b *bot.Bot, chatID int64, a agentInfo) {
		callCount++
	}
	defer func() { notifyBlocked = origNotify }()

	ctx, cancel := context.WithTimeout(context.Background(), 13*time.Second)
	defer cancel()

	agentWatcher(ctx, &bot.Bot{}, 0)

	if callCount != 0 {
		t.Errorf("expected 0 notifications for steady state, got %d", callCount)
	}
}

func TestNotificationIfAlreadyBlocked(t *testing.T) {
	callCount := 0
	herdrAgentList = func() (string, error) {
		return fmt.Sprintf(sampleAgentListJSON, "blocked", "idle"), nil
	}

	origNotify := notifyBlocked
	notifyBlocked = func(ctx context.Context, b *bot.Bot, chatID int64, a agentInfo) {
		callCount++
	}
	defer func() { notifyBlocked = origNotify }()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	agentWatcher(ctx, &bot.Bot{}, 0)

	if callCount != 1 {
		t.Errorf("expected 1 recovery notification for already-blocked agent, got %d", callCount)
	}
}

func TestNoNotificationBlockedToIdle(t *testing.T) {
	callCount := 0
	callNum := 0
	herdrAgentList = func() (string, error) {
		callNum++
		switch callNum {
		case 1:
			return fmt.Sprintf(sampleAgentListJSON, "blocked", "idle"), nil
		default:
			return fmt.Sprintf(sampleAgentListJSON, "idle", "idle"), nil
		}
	}

	origNotify := notifyBlocked
	notifyBlocked = func(ctx context.Context, b *bot.Bot, chatID int64, a agentInfo) {
		callCount++
	}
	defer func() { notifyBlocked = origNotify }()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	agentWatcher(ctx, &bot.Bot{}, 0)

	if callCount != 1 {
		t.Errorf("expected only the initial blocked notification, got %d", callCount)
	}
}

func TestMultipleTransitionsToBlocked(t *testing.T) {
	// Both agents becoming blocked simultaneously = 2 notifications.
	callCount := 0
	var agents []string
	callNum := 0

	herdrAgentList = func() (string, error) {
		callNum++
		switch callNum {
		case 1:
			return fmt.Sprintf(sampleAgentListJSON, "idle", "idle"), nil
		default:
			return fmt.Sprintf(sampleAgentListJSON, "blocked", "blocked"), nil
		}
	}

	origNotify := notifyBlocked
	notifyBlocked = func(ctx context.Context, b *bot.Bot, chatID int64, a agentInfo) {
		callCount++
		agents = append(agents, a.Agent)
	}
	defer func() { notifyBlocked = origNotify }()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	agentWatcher(ctx, &bot.Bot{}, 0)

	if callCount != 2 {
		t.Errorf("expected 2 notifications (both blocked), got %d; agents: %v", callCount, agents)
	}
}

func TestSessionKeyDeduplication(t *testing.T) {
	// Same pane_id, different session_id = new session.
	// New session starting in "blocked" should trigger a fresh notification.
	callCount := 0
	callNum := 0

	herdrAgentList = func() (string, error) {
		callNum++
		switch callNum {
		case 1:
			return fmt.Sprintf(sampleAgentListJSON, "idle", "idle"), nil
		default:
			return `{"id":"cli:agent:list","result":{"agents":[{"agent":"claude","agent_session":{"agent":"claude","kind":"id","source":"herdr:claude","value":"ses-333"},"agent_status":"blocked","cwd":"/home/user/proj","focused":true,"pane_id":"wA:p1","tab_id":"wA:t1","terminal_id":"term_1","workspace_id":"wA"}]}}`, nil
		}
	}

	origNotify := notifyBlocked
	notifyBlocked = func(ctx context.Context, b *bot.Bot, chatID int64, a agentInfo) {
		callCount++
	}
	defer func() { notifyBlocked = origNotify }()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	agentWatcher(ctx, &bot.Bot{}, 0)

	if callCount != 1 {
		t.Errorf("expected 1 notification for new blocked session, got %d", callCount)
	}
}

func TestStalePaneCleanup(t *testing.T) {
	// A pane disappearing from the list should clean up prevStatus entry.
	callCount := 0
	callNum := 0

	herdrAgentList = func() (string, error) {
		callNum++
		switch callNum {
		case 1:
			return fmt.Sprintf(sampleAgentListJSON, "idle", "idle"), nil
		default:
			return `{"id":"cli:agent:list","result":{"agents":[{"agent":"claude","agent_session":{"agent":"claude","kind":"id","source":"herdr:claude","value":"ses-111"},"agent_status":"idle","cwd":"/home/user/proj","focused":true,"pane_id":"wA:p1","tab_id":"wA:t1","terminal_id":"term_1","workspace_id":"wA"}]}}`, nil
		}
	}

	origNotify := notifyBlocked
	notifyBlocked = func(ctx context.Context, b *bot.Bot, chatID int64, a agentInfo) {
		callCount++
	}
	defer func() { notifyBlocked = origNotify }()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	agentWatcher(ctx, &bot.Bot{}, 0)

	if callCount != 0 {
		t.Errorf("expected 0 notifications for pane removal, got %d", callCount)
	}
}

func TestAgentStatusLowercasing(t *testing.T) {
	const upperStatusJSON = `{"id":"cli:agent:list","result":{"agents":[{"agent":"claude","agent_session":{"agent":"claude","kind":"id","source":"herdr:claude","value":"ses-111"},"agent_status":"BLOCKED","cwd":"/home/user/proj","focused":true,"pane_id":"wA:p1","tab_id":"wA:t1","terminal_id":"term_1","workspace_id":"wA"}]}}`

	var env agentListEnvelope
	if err := json.Unmarshal([]byte(upperStatusJSON), &env); err != nil {
		t.Fatalf("parse envelope: %v", err)
	}
	var lr agentListResult
	if err := json.Unmarshal(env.Result, &lr); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	if len(lr.Agents) > 0 {
		lowered := strings.ToLower(lr.Agents[0].AgentStatus)
		if lowered != "blocked" {
			t.Errorf("expected lowered 'blocked', got '%s'", lowered)
		}
	}
}

func TestAgentTrackingIdentityWithoutSession(t *testing.T) {
	first := agentInfo{TerminalID: "term-1", StateChangeSeq: 10}
	replacement := agentInfo{TerminalID: "term-1", StateChangeSeq: 11}
	if agentTrackingIdentity(first) == agentTrackingIdentity(replacement) {
		t.Fatal("sessionless replacement reused watcher identity")
	}
	withSession := agentInfo{TerminalID: "term-1", StateChangeSeq: 12, AgentSession: agentSession{Value: "session-1"}}
	if got := agentTrackingIdentity(withSession); got != "session:session-1" {
		t.Errorf("identity = %q, want session identity", got)
	}
}

func TestBlockedActionKeyboardAlwaysOffersActions(t *testing.T) {
	agent := agentInfo{PaneID: "w1:p1", TerminalID: "term-1", Agent: "claude", StateChangeSeq: 10}
	keyboard := blockedActionKeyboard(agent, nil)
	if keyboard == nil || len(keyboard.InlineKeyboard) != 2 {
		t.Fatalf("unexpected blocked keyboard: %+v", keyboard)
	}
	callbacks := []string{
		keyboard.InlineKeyboard[0][0].CallbackData,
		keyboard.InlineKeyboard[0][1].CallbackData,
		keyboard.InlineKeyboard[1][0].CallbackData,
	}
	for i, prefix := range []string{"al|prompt|", "al|output|", "al|open|"} {
		if !strings.HasPrefix(callbacks[i], prefix) {
			t.Errorf("callback %d = %q, want prefix %q", i, callbacks[i], prefix)
		}
	}
}

func TestBlockedTextChoiceKeyboard(t *testing.T) {
	agent := agentInfo{PaneID: "w1:p1", TerminalID: "term-1", Agent: "claude", StateChangeSeq: 10}
	choices := &parsedChoices{ActiveIndex: -1, Choices: []parsedChoice{
		{CleanText: "Approve once", SubmitText: "Approve once"},
		{CleanText: "Reject", SubmitText: "Reject"},
		{CleanText: "Type your own answer"},
	}}
	keyboard := blockedTextChoiceKeyboard(agent, choices)
	if keyboard == nil || len(keyboard.InlineKeyboard) != 2 {
		t.Fatalf("unexpected text-choice keyboard: %+v", keyboard)
	}
	if !strings.HasPrefix(keyboard.InlineKeyboard[0][0].CallbackData, "br|") {
		t.Errorf("unexpected callback: %q", keyboard.InlineKeyboard[0][0].CallbackData)
	}
	if !strings.Contains(keyboard.InlineKeyboard[0][0].Text, "Approve once") {
		t.Errorf("choice label missing: %q", keyboard.InlineKeyboard[0][0].Text)
	}
}

// TestWatcherDrainsSlowNotifications proves the async dispatch + WaitGroup
// shutdown work: a slow notifyBlocked (simulating a herdr read + Telegram send)
// is still counted by the time agentWatcher returns. Notifications now run on a
// dedicated goroutine, so this also confirms the poll loop no longer blocks on
// notifyBlocked directly.
func TestWatcherDrainsSlowNotifications(t *testing.T) {
	callCount := 0
	callNum := 0
	herdrAgentList = func() (string, error) {
		callNum++
		switch callNum {
		case 1:
			return fmt.Sprintf(sampleAgentListJSON, "idle", "idle"), nil
		default:
			return fmt.Sprintf(sampleAgentListJSON, "blocked", "idle"), nil
		}
	}
	origNotify := notifyBlocked
	notifyBlocked = func(ctx context.Context, b *bot.Bot, chatID int64, a agentInfo) {
		time.Sleep(300 * time.Millisecond)
		callCount++
	}
	defer func() { notifyBlocked = origNotify }()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	agentWatcher(ctx, &bot.Bot{}, 0)

	if callCount != 1 {
		t.Errorf("expected 1 notification after slow notify, got %d", callCount)
	}
}
