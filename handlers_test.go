package main

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// TestParseStartAgentArgs covers the /startagent tokenization, including the
// cases the old strings.Contains(rest, "--") splitter got wrong: a "--" inside
// a command flag must not be treated as the name/command separator.
func TestParseStartAgentArgs(t *testing.T) {
	tests := []struct {
		in                 string
		name, kind, paneID string
		agentArgs          []string
	}{
		{"helper opencode w1:p2", "helper", "opencode", "w1:p2", nil},
		{"helper claude w1:p2 -- --model sonnet", "helper", "claude", "w1:p2", []string{"--model", "sonnet"}},
		{"helper codex w1:p2 --full-auto", "helper", "codex", "w1:p2", []string{"--full-auto"}},
		{"helper opencode w1:p2 --", "helper", "opencode", "w1:p2", nil},
		{"helper", "", "", "", nil},
		{"", "", "", "", nil},
	}
	for _, tt := range tests {
		name, kind, paneID, agentArgs := parseStartAgentArgs(tt.in)
		if name != tt.name || kind != tt.kind || paneID != tt.paneID {
			t.Errorf("parseStartAgentArgs(%q) = (%q, %q, %q), want (%q, %q, %q)", tt.in, name, kind, paneID, tt.name, tt.kind, tt.paneID)
		}
		if !reflect.DeepEqual(agentArgs, tt.agentArgs) {
			t.Errorf("parseStartAgentArgs(%q) agentArgs = %v, want %v", tt.in, agentArgs, tt.agentArgs)
		}
	}
}

func TestNormalizeAgentReadOutput(t *testing.T) {
	plain := "line one\nline two"
	if got := normalizeAgentReadOutput(plain); got != plain {
		t.Errorf("plain output = %q, want %q", got, plain)
	}
	legacy := `{"id":"cli:agent:read","result":{"read":{"text":"legacy text","pane_id":"w1:p1"}}}`
	if got := normalizeAgentReadOutput(legacy); got != "legacy text" {
		t.Errorf("legacy output = %q, want legacy text", got)
	}
}

func TestAgentStartCommandArgs(t *testing.T) {
	want := []string{"agent", "start", "reviewer", "--kind", "claude", "--pane", "w1:p2", "--", "--model", "sonnet"}
	got := agentStartCommandArgs("reviewer", "claude", "w1:p2", "--model", "sonnet")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("agentStartCommandArgs = %v, want %v", got, want)
	}
}

// TestChoiceKeys locks in the arrow-key sequence that drives TUI selection
// menus: option 1 is just Enter, option N is (N-1) Downs then Enter.
func TestChoiceKeys(t *testing.T) {
	tests := []struct {
		active, selected int
		want             []string
	}{
		{0, 1, []string{"Enter"}},
		{0, 3, []string{"Down", "Down", "Enter"}},
		{2, 1, []string{"Up", "Up", "Enter"}},
		{2, 3, []string{"Enter"}},
	}
	for _, tt := range tests {
		got := choiceKeys(tt.active, tt.selected)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("choiceKeys(%d, %d) = %v, want %v", tt.active, tt.selected, got, tt.want)
		}
	}
}

func TestDeliverCursorChoiceSendsAndVerifiesOneKeyAtATime(t *testing.T) {
	choices := []parsedChoice{{CleanText: "First"}, {CleanText: "Second"}, {CleanText: "Third"}}
	initial := &parsedChoices{Prompt: "Pick one", ActiveIndex: 0, Choices: choices}
	active := 0
	var sent []string
	err := deliverCursorChoice(
		initial,
		3,
		func(key string) error {
			sent = append(sent, key)
			if key == "Down" {
				active++
			}
			return nil
		},
		func() *parsedChoices {
			return &parsedChoices{Prompt: "Pick one", ActiveIndex: active, Choices: choices}
		},
		func() error { return nil },
		func() {},
	)
	if err != nil {
		t.Fatalf("deliverCursorChoice failed: %v", err)
	}
	want := []string{"Down", "Down", "Enter"}
	if !reflect.DeepEqual(sent, want) {
		t.Fatalf("keys sent = %v, want separate calls %v", sent, want)
	}
}

func TestDeliverCursorChoiceDoesNotSubmitIfCursorDoesNotMove(t *testing.T) {
	choices := []parsedChoice{{CleanText: "First"}, {CleanText: "Second"}}
	initial := &parsedChoices{Prompt: "Pick one", ActiveIndex: 0, Choices: choices}
	var sent []string
	err := deliverCursorChoice(
		initial,
		2,
		func(key string) error { sent = append(sent, key); return nil },
		func() *parsedChoices { return &parsedChoices{Prompt: "Pick one", ActiveIndex: 0, Choices: choices} },
		func() error { return nil },
		func() {},
	)
	if err == nil {
		t.Fatal("expected cursor verification failure")
	}
	if !reflect.DeepEqual(sent, []string{"Down"}) {
		t.Fatalf("unsafe keys sent after cursor failed to move: %v", sent)
	}
}

func TestDeliverCursorChoiceUsesClaudeNumericJumpThenEnter(t *testing.T) {
	choices := []parsedChoice{{CleanText: "First", NavigateKey: "1"}, {CleanText: "Second", NavigateKey: "2"}, {CleanText: "Third", NavigateKey: "3"}}
	initial := &parsedChoices{Prompt: "Pick one", ActiveIndex: 0, Choices: choices}
	active := 0
	var sent []string
	err := deliverCursorChoice(
		initial,
		3,
		func(key string) error {
			sent = append(sent, key)
			if key == "3" {
				active = 2
			}
			return nil
		},
		func() *parsedChoices {
			return &parsedChoices{Prompt: "Pick one", ActiveIndex: active, Choices: choices}
		},
		func() error { return nil },
		func() {},
	)
	if err != nil {
		t.Fatalf("deliverCursorChoice failed: %v", err)
	}
	if !reflect.DeepEqual(sent, []string{"3", "Enter"}) {
		t.Fatalf("keys sent = %v", sent)
	}
}

func TestFormatAgentFromGet(t *testing.T) {
	t.Run("valid agent", func(t *testing.T) {
		in := `{"id":"cli:agent:get","result":{"agent":{"name":"reviewer","agent":"claude","agent_status":"blocked","pane_id":"wA:p1","terminal_id":"term_1","workspace_id":"wA","cwd":"/home/user/proj"}}}`
		got := formatAgentFromGet(in)
		want := "<b>reviewer</b>\n\nStatus: blocked\nPane: wA:p1\nWorkspace: wA\nCwd: /home/user/proj"
		if got != want {
			t.Errorf("formatAgentFromGet(valid) = %q, want %q", got, want)
		}
	})
	t.Run("unparseable falls back to raw escaped", func(t *testing.T) {
		got := formatAgentFromGet("not json <at> all")
		want := "<pre><code>not json &lt;at&gt; all</code></pre>"
		if got != want {
			t.Errorf("formatAgentFromGet(fallback) = %q, want %q", got, want)
		}
	})
	t.Run("escapes HTML in fields", func(t *testing.T) {
		in := `{"id":"x","result":{"agent":{"agent":"<a&b>","agent_status":"idle","pane_id":"p","workspace_id":"w","cwd":"/c"}}}`
		got := formatAgentFromGet(in)
		if !strings.Contains(got, "<b>&lt;a&amp;b&gt;</b>") {
			t.Errorf("expected escaped agent name in %q", got)
		}
	})
}

func TestShortenPathIn(t *testing.T) {
	tests := []struct {
		path, home, want string
	}{
		{"/home/user/proj", "/home/user", "~/proj"},
		{"/home/user", "/home/user", "~"},
		{"/home/user/sub/deep", "/home/user", "~/sub/deep"},
		// Boundary: /home/user must not match /home/user2.
		{"/home/user2/proj", "/home/user", "/home/user2/proj"},
		{"/var/other", "/home/user", "/var/other"},
		{"/anything", "", "/anything"},
	}
	for _, tt := range tests {
		got := shortenPathIn(tt.path, tt.home)
		if got != tt.want {
			t.Errorf("shortenPathIn(%q, %q) = %q, want %q", tt.path, tt.home, got, tt.want)
		}
	}
}

func TestEscapeHTML(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain", "plain"},
		{"a & b", "a &amp; b"},
		{"<tag>", "&lt;tag&gt;"},
		{"a<b>&c", "a&lt;b&gt;&amp;c"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := escapeHTML(tt.in); got != tt.want {
			t.Errorf("escapeHTML(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestEscapeHTMLLimitedKeepsEntitiesWhole(t *testing.T) {
	if got := escapeHTMLLimited("&&&&", 6); got != "&amp;…" {
		t.Errorf("escapeHTMLLimited = %q", got)
	}
}

func TestSanitizeTTY(t *testing.T) {
	// Terminal controls are stripped while prompt line breaks and tabs remain.
	in := "clean\x00text\x07bell\nline2\r\ttab"
	want := "cleantextbell\nline2\ttab"
	if got := sanitizeTTY(in); got != want {
		t.Errorf("sanitizeTTY = %q, want %q", got, want)
	}
}

func TestParseSendRequestPreservesPromptLayout(t *testing.T) {
	target, prompt := parseSendRequest("/send reviewer First line\n  indented second line")
	if target != "reviewer" {
		t.Errorf("target = %q", target)
	}
	if prompt != "First line\n  indented second line" {
		t.Errorf("prompt = %q", prompt)
	}
	if target, prompt := parseSendRequest("/send reviewer"); target != "" || prompt != "" {
		t.Errorf("incomplete request = (%q, %q)", target, prompt)
	}
}

func TestParseCommandArgs(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"/send agent hi", []string{"/send", "agent", "hi"}},
		{"  /read   x   10  ", []string{"/read", "x", "10"}},
	}
	for _, tt := range tests {
		got := parseCommandArgs(tt.in)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("parseCommandArgs(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

// TestDefaultHandlerNilFromNoPanic guards the nil-From fix: a message whose
// From is nil (e.g. anonymous group admin) must not crash the bot.
func TestDefaultHandlerNilFromNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("defaultHandler panicked on nil From: %v", r)
		}
	}()
	// From is nil; the handler must return before touching the bot or cfgGlobal.
	defaultHandler(context.Background(), &bot.Bot{}, &models.Update{Message: &models.Message{}})
}

// TestOwnerAuthNilMessage: ownerAuth must reject updates without a Message/From
// without touching the bot.
func TestOwnerAuthNilMessage(t *testing.T) {
	if ownerAuth(context.Background(), &bot.Bot{}, &models.Update{}) {
		t.Error("expected ownerAuth=false for update with no Message")
	}
}

func TestIsAuthorizedRequiresOwnerAndPrivateChat(t *testing.T) {
	original := cfgGlobal
	cfgGlobal = &Config{OwnerID: 42, ChatID: 42}
	defer func() { cfgGlobal = original }()

	if !isAuthorized(42, 42) {
		t.Error("expected configured owner in configured chat to be authorized")
	}
	if isAuthorized(7, 42) {
		t.Error("expected another user to be rejected")
	}
	if isAuthorized(42, -100123) {
		t.Error("expected owner in another chat to be rejected")
	}
}

const emptyAgentListJSON = `{"id":"cli:agent:list","result":{"agents":[]}}`

func TestBuildAgentListEmpty(t *testing.T) {
	orig := herdrAgentList
	herdrAgentList = func() (string, error) { return emptyAgentListJSON, nil }
	defer func() { herdrAgentList = orig }()

	msg, kb, err := buildAgentList()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "0 agents") {
		t.Errorf("expected empty-list message, got %q", msg)
	}
	if kb == nil || len(kb.InlineKeyboard) < 4 {
		t.Fatalf("expected dashboard controls, got %+v", kb)
	}
	last := kb.InlineKeyboard[len(kb.InlineKeyboard)-1]
	if len(last) != 2 || last[0].CallbackData != "al|list|all|0" || last[1].CallbackData != "al|help" {
		t.Errorf("unexpected refresh/help row: %+v", last)
	}
}

func TestBuildAgentList(t *testing.T) {
	orig := herdrAgentList
	herdrAgentList = func() (string, error) {
		return fmt.Sprintf(sampleAgentListJSON, "idle", "idle"), nil
	}
	defer func() { herdrAgentList = orig }()

	msg, kb, err := buildAgentList()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "2 agents") {
		t.Errorf("expected count header, got %q", msg)
	}
	if kb == nil || len(kb.InlineKeyboard) < 6 {
		t.Fatalf("expected agent rows and dashboard controls, got %v", kb)
	}
	// Each agent is represented by one readable open button backed by a token.
	row := kb.InlineKeyboard[0]
	if len(row) != 1 {
		t.Fatalf("expected 1 button in row 0, got %d", len(row))
	}
	if !strings.Contains(row[0].Text, "reviewer") || !strings.HasPrefix(row[0].CallbackData, "al|open|") {
		t.Errorf("unexpected first agent button: %+v", row[0])
	}
	last := kb.InlineKeyboard[len(kb.InlineKeyboard)-1]
	if len(last) != 2 || last[0].CallbackData != "al|list|all|0" || last[1].CallbackData != "al|help" {
		t.Errorf("unexpected last row: %+v", last)
	}
}

func TestBuildAgentDetail(t *testing.T) {
	agent := agentInfo{
		Name: "reviewer", Agent: "claude", AgentStatus: "blocked",
		PaneID: "wA:p1", TerminalID: "term-1", WorkspaceID: "wA", Cwd: "/tmp/project",
	}
	message, keyboard, err := buildAgentDetail(agent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(message, "reviewer") || !strings.Contains(message, "Needs input") || !strings.Contains(message, "wA:p1") {
		t.Errorf("unexpected detail message: %q", message)
	}
	if keyboard == nil || len(keyboard.InlineKeyboard) != 4 {
		t.Fatalf("unexpected detail keyboard: %+v", keyboard)
	}
	wantActions := []string{"al|prompt|", "al|output|", "al|open|", "al|interrupt|", "al|request_close|", "al|back"}
	var callbacks []string
	for _, row := range keyboard.InlineKeyboard {
		for _, button := range row {
			callbacks = append(callbacks, button.CallbackData)
		}
	}
	for i, prefix := range wantActions {
		if !strings.HasPrefix(callbacks[i], prefix) {
			t.Errorf("callback %d = %q, want prefix %q", i, callbacks[i], prefix)
		}
		if len(callbacks[i]) > 64 {
			t.Errorf("callback exceeds Telegram limit: %q", callbacks[i])
		}
	}
}

func TestValidateAgentSnapshotRejectsSessionlessReplacement(t *testing.T) {
	expected := agentInfo{Agent: "claude", PaneID: "w1:p1", TerminalID: "term-1", StateChangeSeq: 10}
	replacement := expected
	replacement.StateChangeSeq = 11
	if err := validateAgentSnapshot(expected, replacement); err == nil {
		t.Fatal("sessionless replacement was accepted")
	}
	if err := validateAgentSnapshot(expected, expected); err != nil {
		t.Fatalf("unchanged sessionless agent rejected: %v", err)
	}
}

func TestAgentUILabelDisambiguatesUnnamedAgents(t *testing.T) {
	first := agentInfo{Agent: "opencode", PaneID: "w1:p1"}
	second := agentInfo{Agent: "opencode", PaneID: "w1:p2"}
	if agentUILabel(first) == agentUILabel(second) {
		t.Fatal("unnamed agents received identical UI labels")
	}
	if got := agentUILabel(agentInfo{Name: "reviewer", Agent: "claude", PaneID: "w1:p3"}); got != "reviewer" {
		t.Errorf("named agent label = %q", got)
	}
}

func TestPendingPromptReplyMatchCapturesCommands(t *testing.T) {
	original := pendingPrompts
	pendingPrompts = newPromptRegistry(time.Minute, 10)
	defer func() { pendingPrompts = original }()
	pendingPrompts.register(42, 7, 42, agentInfo{PaneID: "w1:p1", TerminalID: "term-1"})
	update := &models.Update{Message: &models.Message{
		Text:           "/close anything",
		From:           &models.User{ID: 42},
		Chat:           models.Chat{ID: 42},
		ReplyToMessage: &models.Message{ID: 7},
	}}
	if !pendingPromptReplyMatch(update) {
		t.Fatal("command-like ForceReply was not captured")
	}
	update.Message.Text = ""
	if pendingPromptReplyMatch(update) {
		t.Fatal("empty reply matched pending prompt")
	}
	if !pendingPrompts.has(42, 7, 42) {
		t.Fatal("empty reply consumed pending prompt")
	}
}
