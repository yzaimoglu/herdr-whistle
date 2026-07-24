package main

import (
	"fmt"
	"strconv"
	"testing"
)

func TestParseBoxChoicesBasic(t *testing.T) {
	input := "\u2502\n\u2502  Which color do you prefer?\n\u2502\n\u2502  1. Red\n\u2502  2. Blue\n\u2502  3. Green\n\u2502\n"
	pc := parseBoxChoices(input)
	if pc == nil {
		t.Fatal("parseBoxChoices returned nil, expected valid parsedChoices")
	}
	if pc.Prompt != "Which color do you prefer?" {
		t.Errorf("expected prompt 'Which color do you prefer?', got '%s'", pc.Prompt)
	}
	if len(pc.Choices) != 3 {
		t.Fatalf("expected 3 choices, got %d", len(pc.Choices))
	}
	if pc.Choices[0].CleanText != "Red" {
		t.Errorf("expected choice 0 'Red', got '%s'", pc.Choices[0].CleanText)
	}
	if pc.Choices[1].CleanText != "Blue" {
		t.Errorf("expected choice 1 'Blue', got '%s'", pc.Choices[1].CleanText)
	}
	if pc.Choices[2].CleanText != "Green" {
		t.Errorf("expected choice 2 'Green', got '%s'", pc.Choices[2].CleanText)
	}
}

func TestParseBoxChoicesHeavyBorder(t *testing.T) {
	input := "┃\n┃  Choose a deployment\n┃\n┃  1. Staging\n┃  2. Production\n┃\n"
	pc := parseBoxChoices(input)
	if pc == nil || pc.Prompt != "Choose a deployment" || len(pc.Choices) != 2 {
		t.Fatalf("heavy-border choices not parsed: %+v", pc)
	}
}

func TestParseBoxChoicesWithContinuationLines(t *testing.T) {
	input := "\u2502\n\u2502  Which framework?\n\u2502\n\u2502  1. React\n\u2502     A JavaScript library for building user interfaces\n\u2502  2. Vue\n\u2502     Another framework with a gentle learning curve\n\u2502  3. Svelte\n\u2502\n"
	pc := parseBoxChoices(input)
	if pc == nil {
		t.Fatal("parseBoxChoices returned nil, expected valid parsedChoices")
	}
	if pc.Prompt != "Which framework?" {
		t.Errorf("expected prompt 'Which framework?', got '%s'", pc.Prompt)
	}
	if len(pc.Choices) != 3 {
		t.Fatalf("expected 3 choices, got %d", len(pc.Choices))
	}
	if pc.Choices[0].CleanText != "React" {
		t.Errorf("expected choice 0 'React', got '%s'", pc.Choices[0].CleanText)
	}
	if pc.Choices[1].CleanText != "Vue" {
		t.Errorf("expected choice 1 'Vue', got '%s'", pc.Choices[1].CleanText)
	}
	if pc.Choices[2].CleanText != "Svelte" {
		t.Errorf("expected choice 2 'Svelte', got '%s'", pc.Choices[2].CleanText)
	}
}

func TestParseBoxChoicesWithParentheticalNumbering(t *testing.T) {
	input := "\u2502\n\u2502  Pick one:\n\u2502\n\u2502  1) Option Alpha\n\u2502  2) Option Beta\n\u2502  3) Option Gamma\n\u2502\n"
	pc := parseBoxChoices(input)
	if pc == nil {
		t.Fatal("parseBoxChoices returned nil")
	}
	if len(pc.Choices) != 3 {
		t.Fatalf("expected 3 choices, got %d", len(pc.Choices))
	}
	if pc.Choices[0].CleanText != "Option Alpha" {
		t.Errorf("expected 'Option Alpha', got '%s'", pc.Choices[0].CleanText)
	}
}

func TestParseBoxChoicesWithTypeYourOwn(t *testing.T) {
	input := "\u2502\n\u2502  How to proceed?\n\u2502\n\u2502  1. Continue\n\u2502  2. Stop\n\u2502  3. Type your own answer\n\u2502\n"
	pc := parseBoxChoices(input)
	if pc == nil {
		t.Fatal("parseBoxChoices returned nil")
	}
	if len(pc.Choices) != 3 {
		t.Fatalf("expected 3 choices, got %d", len(pc.Choices))
	}
	if pc.Choices[2].CleanText != "Type your own answer" {
		t.Errorf("expected 'Type your own answer', got '%s'", pc.Choices[2].CleanText)
	}
}

func TestParseBoxChoicesWithHeaderLines(t *testing.T) {
	input := "\u2192 Asked 1 question\n\u25A3  Sisyphus - Ultraworker \u00B7 Big Pickle\n\u2502\n\u2502  Which approach?\n\u2502\n\u2502  1. Option A\n\u2502     Description of option A\n\u2502  2. Option B\n\u2502  3. Type your own answer\n\u2502\n"
	pc := parseBoxChoices(input)
	if pc == nil {
		t.Fatal("parseBoxChoices returned nil")
	}
	if pc.Prompt != "Which approach?" {
		t.Errorf("expected prompt 'Which approach?', got '%s'", pc.Prompt)
	}
	if len(pc.Choices) != 3 {
		t.Fatalf("expected 3 choices, got %d", len(pc.Choices))
	}
}

func TestParseBoxChoicesNoBoxContent(t *testing.T) {
	input := "just some random text\nwith no box drawing chars"
	pc := parseBoxChoices(input)
	if pc != nil {
		t.Fatal("expected nil for non-box content")
	}
}

func TestParseBoxChoicesTooFewLines(t *testing.T) {
	input := "\u2502\n\u2502  only one line\n"
	pc := parseBoxChoices(input)
	if pc != nil {
		t.Fatal("expected nil for too few lines")
	}
}

func TestParseBoxChoicesNoChoices(t *testing.T) {
	input := "\u2502\n\u2502  Question only?\n\u2502\n\u2502  No actual choices here\n"
	pc := parseBoxChoices(input)
	if pc != nil {
		t.Fatal("expected nil when no numbered choices present")
	}
}

func TestParseBoxChoicesReturnsCorrectPrompt(t *testing.T) {
	input := "\u2502\n\u2502  What is your favorite programming language?\n\u2502\n\u2502  1. Go\n\u2502  2. Rust\n\u2502  3. TypeScript\n\u2502\n"
	pc := parseBoxChoices(input)
	if pc == nil {
		t.Fatal("parseBoxChoices returned nil")
	}
	if pc.Prompt != "What is your favorite programming language?" {
		t.Errorf("expected 'What is your favorite programming language?', got '%s'", pc.Prompt)
	}
}

// TestParseChoicesClackSingleSelect guards the @clack single-select format:
// only the active option carries the \u276f cursor; inactive options are plain.
// All options must be captured -- a naive "require a cursor" filter would
// drop the inactive ones.
func TestParseChoicesClackSingleSelect(t *testing.T) {
	input := "? Select a framework\n" +
		"\u2502\n" +
		"\u2502  \u276f  Next.js\n" +
		"\u2502     Nuxt\n" +
		"\u2502     Remix\n" +
		"\u2502\n" +
		"\u2514  \u2191\u2193 to navigate \u00b7 enter to select\n"
	pc := parseChoices(input)
	if pc == nil {
		t.Fatal("parseChoices returned nil")
	}
	if pc.Prompt != "? Select a framework" {
		t.Errorf("expected prompt '? Select a framework', got '%s'", pc.Prompt)
	}
	if len(pc.Choices) != 3 {
		t.Fatalf("expected 3 choices, got %d: %+v", len(pc.Choices), pc.Choices)
	}
	if pc.ActiveIndex != 0 {
		t.Errorf("expected active index 0, got %d", pc.ActiveIndex)
	}
	want := []string{"Next.js", "Nuxt", "Remix"}
	for i, w := range want {
		if pc.Choices[i].CleanText != w {
			t.Errorf("choice %d: expected '%s', got '%s'", i, w, pc.Choices[i].CleanText)
		}
	}
}

func TestParseChoicesClaudePermissionPrompt(t *testing.T) {
	input := "Do you want to proceed?\n" +
		"❯ 1. Yes\n" +
		"  2. Yes, and don't ask again\n" +
		"  3. No\n" +
		"Enter to select · Tab/Arrow keys to navigate\n" +
		"Esc to cancel\n"
	pc := parseChoices(input)
	if pc == nil {
		t.Fatal("Claude permission prompt was not parsed")
	}
	if pc.Prompt != "Do you want to proceed?" || pc.ActiveIndex != 0 {
		t.Fatalf("unexpected Claude prompt: %+v", pc)
	}
	want := []string{"Yes", "Yes, and don't ask again", "No"}
	if len(pc.Choices) != len(want) {
		t.Fatalf("Claude choices = %+v", pc.Choices)
	}
	for i := range want {
		if pc.Choices[i].CleanText != want[i] {
			t.Errorf("choice %d = %q, want %q", i, pc.Choices[i].CleanText, want[i])
		}
	}
}

func TestParseChoicesCodexApprovalPrompt(t *testing.T) {
	input := "Allow command?\n" +
		"› 1. Yes, proceed\n" +
		"  2. No, and tell Codex what to do differently\n" +
		"Press enter to confirm or esc to cancel\n"
	pc := parseChoices(input)
	if pc == nil {
		t.Fatal("Codex approval prompt was not parsed")
	}
	if pc.Prompt != "Allow command?" || pc.ActiveIndex != 0 {
		t.Fatalf("unexpected Codex prompt: %+v", pc)
	}
	want := []string{"Yes, proceed", "No, and tell Codex what to do differently"}
	if len(pc.Choices) != len(want) {
		t.Fatalf("Codex choices = %+v", pc.Choices)
	}
	for i := range want {
		if pc.Choices[i].CleanText != want[i] {
			t.Errorf("choice %d = %q, want %q", i, pc.Choices[i].CleanText, want[i])
		}
	}
}

func TestParseBinaryChoicesCodexFallback(t *testing.T) {
	pc := parseAgentChoices("Would you like to continue? [y/n]\n")
	if pc == nil || pc.Prompt != "Would you like to continue? [y/n]" || len(pc.Choices) != 2 {
		t.Fatalf("binary Codex prompt not parsed: %+v", pc)
	}
	if pc.Choices[0].CleanText != "Yes" || pc.Choices[1].CleanText != "No" {
		t.Fatalf("unexpected binary choices: %+v", pc.Choices)
	}
	if pc.Choices[0].SubmitText != "y" || pc.Choices[1].SubmitText != "n" {
		t.Fatalf("unexpected binary input values: %+v", pc.Choices)
	}
}

func TestParseClaudePermissionWithoutNavigationFooter(t *testing.T) {
	input := "Bash command\nrm -rf build\n\nDo you want to proceed?\n❯ Yes\n  No\nTab to amend · Ctrl+E to explain\nEsc to cancel\n"
	pc := parseAgentChoices(input)
	if pc == nil || len(pc.Choices) != 2 || pc.ActiveIndex != 0 {
		t.Fatalf("Claude permission prompt not parsed: %+v", pc)
	}
	if pc.Choices[0].CleanText != "Yes" || pc.Choices[1].CleanText != "No" {
		t.Fatalf("unexpected Claude permission choices: %+v", pc.Choices)
	}
}

func TestParseCursorlessClaudePermissionUsesBinaryInput(t *testing.T) {
	input := "Do you want to proceed?\n1. Yes\n2. No\nEnter to select · Arrow keys to navigate\nEsc to cancel\n"
	pc := parseAgentChoicesFor("claude", input)
	if pc == nil || len(pc.Choices) != 2 {
		t.Fatalf("cursorless Claude permission not parsed: %+v", pc)
	}
	if pc.Choices[0].SubmitText != "y" || pc.Choices[1].SubmitText != "n" {
		t.Fatalf("cursorless inputs = %+v", pc.Choices)
	}
}

func TestParseOpenCodePermissionButtons(t *testing.T) {
	input := "△ Permission required\n  # Shell command\n  $ git status\n\n  Allow once  Allow always  Reject        ⇆ select  enter confirm\n"
	pc := parseAgentChoicesFor("opencode", input)
	if pc == nil || pc.Prompt != "# Shell command" || len(pc.Choices) != 2 {
		t.Fatalf("OpenCode permission prompt not parsed: %+v", pc)
	}
	if pc.Choices[0].CleanText != "Allow once" || pc.Choices[0].DirectKey != "Enter" {
		t.Fatalf("unexpected allow action: %+v", pc.Choices[0])
	}
	if pc.Choices[1].CleanText != "Reject" || pc.Choices[1].DirectKey != "Escape" {
		t.Fatalf("unexpected reject action: %+v", pc.Choices[1])
	}
	if kb := buildChoiceKeyboard(pc, "nonce"); kb == nil || len(kb.InlineKeyboard) != 2 {
		t.Fatalf("OpenCode permission keyboard not built: %+v", kb)
	}
}

func TestOpenCodePermissionParserIsAgentSpecific(t *testing.T) {
	input := "Permission required\nAllow once  Allow always  Reject\n"
	if pc := parseAgentChoicesFor("claude", input); pc != nil {
		t.Fatalf("OpenCode permission actions exposed for Claude: %+v", pc)
	}
}

func TestParseChoicesRejectsCodexFreeAnswerForm(t *testing.T) {
	input := "What should Codex do?\n> first answer field\n  second answer field\nEnter to submit answer\n"
	if pc := parseAgentChoicesFor("codex", input); pc != nil {
		t.Fatalf("free-answer form became choices: %+v", pc)
	}
}

func TestChoiceSourceFingerprintCoversVisibleScreen(t *testing.T) {
	first := parseAgentChoicesFor("codex", "Allow command?\n› 1. Yes\n  2. No\nPress enter to confirm or esc to cancel\n")
	second := parseAgentChoicesFor("codex", "Different context\nAllow command?\n› 1. Yes\n  2. No\nPress enter to confirm or esc to cancel\n")
	if first == nil || second == nil || first.SourceFingerprint == second.SourceFingerprint {
		t.Fatal("visible-screen changes did not alter choice fingerprint")
	}
}

// TestParseChoicesStripsBorderAndCursor confirms the \u276f cursor and \u2502 border
// are stripped from the active option's clean text.
func TestParseChoicesStripsBorderAndCursor(t *testing.T) {
	input := "? Pick one\n\n\u276f  First\n   Second\n   Third\n\nenter to select\n"
	pc := parseChoices(input)
	if pc == nil {
		t.Fatal("parseChoices returned nil")
	}
	if pc.Choices[0].CleanText != "First" {
		t.Errorf("expected 'First' (cursor stripped), got '%s'", pc.Choices[0].CleanText)
	}
}

func TestParseChoicesExcludesIndentedDescriptions(t *testing.T) {
	input := "? Select a framework\n" +
		"│  ❯  Next.js\n" +
		"│       React framework description\n" +
		"│     Nuxt\n" +
		"│       Vue framework description\n" +
		"│     Remix\n" +
		"└  ↑↓ to navigate · enter to select\n"
	pc := parseChoices(input)
	if pc == nil {
		t.Fatal("parseChoices returned nil")
	}
	want := []string{"Next.js", "Nuxt", "Remix"}
	if len(pc.Choices) != len(want) {
		t.Fatalf("got choices %+v, want %v", pc.Choices, want)
	}
	for i := range want {
		if pc.Choices[i].CleanText != want[i] {
			t.Errorf("choice %d = %q, want %q", i, pc.Choices[i].CleanText, want[i])
		}
	}
}

// TestParseChoicesNoHelpBar: without a recognizable help bar there is no menu.
func TestParseChoicesNoHelpBar(t *testing.T) {
	input := "? Pick one\n\u276f  A\n   B\n"
	if pc := parseChoices(input); pc != nil {
		t.Fatalf("expected nil for input with no help bar, got %+v", pc)
	}
}

func TestParseChoicesRequiresPrompt(t *testing.T) {
	input := "│  ❯  First\n│     Second\n└  ↑↓ to navigate · enter to select\n"
	if pc := parseChoices(input); pc != nil {
		t.Fatalf("expected nil without a question prompt, got %+v", pc)
	}
}

func TestParseChoicesRejectsSupersededMenu(t *testing.T) {
	input := "? Old question\n❯  Yes\n   No\nenter to select\n\nNew prompt waiting for text\n"
	if pc := parseChoices(input); pc != nil {
		t.Fatalf("expected superseded menu to be rejected, got %+v", pc)
	}
}

// TestParseChoicesAllCursors: when every option carries a cursor (multiselect
// style with \u25cf/\u25cb), all are captured and the cursors stripped.
func TestParseChoicesAllCursors(t *testing.T) {
	input := "? Select items\n" +
		"\u2502\n" +
		"\u2502  \u25cf  Option A\n" +
		"\u2502  \u25cb  Option B\n" +
		"\u2502\n" +
		"\u2514  \u2191\u2193 navigate \u00b7 space to select \u00b7 enter to submit\n"
	pc := parseChoices(input)
	if pc == nil {
		t.Fatal("parseChoices returned nil")
	}
	if len(pc.Choices) != 2 {
		t.Fatalf("expected 2 choices, got %d: %+v", len(pc.Choices), pc.Choices)
	}
	if !pc.MultiSelect {
		t.Error("expected multiselect prompt")
	}
	if pc.Choices[0].CleanText != "Option A" {
		t.Errorf("choice 0: got '%s'", pc.Choices[0].CleanText)
	}
	if pc.Choices[1].CleanText != "Option B" {
		t.Errorf("choice 1: got '%s'", pc.Choices[1].CleanText)
	}
}

// TestBuildChoiceKeyboard verifies the readable one-choice-per-row layout and
// the 1-based callback data that choiceCallbackHandler relies on.
func TestBuildChoiceKeyboard(t *testing.T) {
	pc := &parsedChoices{ActiveIndex: 0, Choices: []parsedChoice{
		{CleanText: "A"}, {CleanText: "B"}, {CleanText: "C"},
		{CleanText: "D"}, {CleanText: "E"}, {CleanText: "F"},
	}}
	kb := buildChoiceKeyboard(pc, "nonce123")
	if kb == nil {
		t.Fatal("nil keyboard")
	}
	if got := len(kb.InlineKeyboard); got != 6 {
		t.Fatalf("expected 6 rows, got %d", got)
	}
	for i, row := range kb.InlineKeyboard {
		if len(row) != 1 {
			t.Fatalf("row %d has %d buttons", i, len(row))
		}
		btn := row[0]
		wantData := fmt.Sprintf("ch|nonce123|%d", i+1)
		if btn.CallbackData != wantData {
			t.Errorf("row %d data=%q, want %q", i, btn.CallbackData, wantData)
		}
		wantText := strconv.Itoa(i+1) + " · " + pc.Choices[i].CleanText
		if btn.Text != wantText {
			t.Errorf("row %d text=%q, want %q", i, btn.Text, wantText)
		}
	}
}

func TestBuildChoiceKeyboardRejectsUnverifiableMenus(t *testing.T) {
	if kb := buildChoiceKeyboard(&parsedChoices{ActiveIndex: -1, Choices: []parsedChoice{{CleanText: "A"}}}, "nonce"); kb != nil {
		t.Error("expected menu with unknown cursor to have no keyboard")
	}
	if kb := buildChoiceKeyboard(&parsedChoices{ActiveIndex: 0, MultiSelect: true, Choices: []parsedChoice{{CleanText: "A"}}}, "nonce"); kb != nil {
		t.Error("expected multiselect menu to have no keyboard")
	}
}
