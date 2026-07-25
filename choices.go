package main

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"
)

// parsedChoice represents a single option in a selection prompt.
type parsedChoice struct {
	CleanText   string // text with selection indicator stripped
	SubmitText  string // exact text input for explicit textual prompts
	DirectKey   string // key that submits the choice immediately
	NavigateKey string // key that focuses the choice without submitting it
	Custom      bool   // choice opens an inline free-form answer
	Skip        bool   // visible utility action that is unsafe to automate
}

// parsedChoices holds the parsed selection prompt.
type parsedChoices struct {
	Prompt            string         // the question line (e.g. "? How to proceed")
	Choices           []parsedChoice // the available options
	ActiveIndex       int            // current cursor position, or -1 when unknown
	MultiSelect       bool           // true when Space toggles multiple options
	SourceFingerprint string         // complete visible-screen identity
}

// choiceIndicators matches lines that start with a selection cursor/symbol.
var choiceIndicators = regexp.MustCompile(`^[>\x{203A}\x{276F}\x{25C6}\x{25CF}\x{25CB}\x{25FB}\x{25A0}\x{25C7}]`)

// helpBarPattern matches the bottom help bar line (case-insensitive for "enter").
var helpBarPattern = regexp.MustCompile(`(?i)(?:enter|return|space).*(?:select|choose|confirm|submit)|(?:↑|↓).*(?:select|choose|submit|navigate)`)

// separatorPattern matches divider/separator lines.
var separatorPattern = regexp.MustCompile(`^[─═\-\s＿_]{4,}$`)

// choiceLineRe matches a numbered choice line: "N. text..." or "N) text..."
var choiceLineRe = regexp.MustCompile(`^(\d+)[.)]\s+(.*)`)
var permissionChoiceRe = regexp.MustCompile(`(?i)^\s*([>›❯]?)\s*(?:\d+[.)]\s*)?(yes\b.*|no\b.*)$`)

func isMenuFooterLine(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	return lower == "" ||
		strings.Contains(lower, "esc to cancel") ||
		strings.Contains(lower, "escape to cancel") ||
		strings.Contains(lower, "to navigate") ||
		strings.Contains(lower, "arrow keys") ||
		strings.Contains(lower, "tab/arrow") ||
		strings.Contains(lower, "enter to select") ||
		strings.Contains(lower, "enter to confirm") ||
		strings.Contains(lower, "enter to submit")
}

func isActiveChoiceRune(r rune) bool {
	return r == '❯' || r == '›' || r == '>'
}

func parseAgentChoices(output string) *parsedChoices {
	return parseAgentChoicesFor("", output)
}

func parseAgentChoicesFor(agent, output string) *parsedChoices {
	if agent == "opencode" {
		if choices := parseOpenCodePermission(output); choices != nil {
			return bindChoicesToSource(choices, output)
		}
	}
	if choices := parsePermissionChoices(output); choices != nil {
		return bindChoicesToSource(decorateAgentChoices(agent, choices), output)
	}
	if choices := parseChoices(output); choices != nil {
		return bindChoicesToSource(decorateAgentChoices(agent, choices), output)
	}
	if agent != "codex" {
		if choices := parseBoxChoices(output); choices != nil {
			return bindChoicesToSource(decorateAgentChoices(agent, choices), output)
		}
	}
	return bindChoicesToSource(parseBinaryChoices(output), output)
}

func decorateAgentChoices(agent string, choices *parsedChoices) *parsedChoices {
	if choices == nil || choices.MultiSelect || len(choices.Choices) > 9 {
		return choices
	}
	for i := range choices.Choices {
		choice := &choices.Choices[i]
		lower := strings.ToLower(strings.TrimSpace(choice.CleanText))
		choice.Custom = strings.HasPrefix(lower, "type something") ||
			strings.Contains(lower, "type your own") ||
			strings.Contains(lower, "custom answer")
		choice.Skip = strings.HasPrefix(lower, "chat about this")
		if choice.Custom || choice.Skip {
			choice.SubmitText = ""
		}
		switch agent {
		case "opencode":
			choice.SubmitText = ""
			if choice.Custom {
				choice.Skip = true
				continue
			}
			choice.DirectKey = strconv.Itoa(i + 1)
		case "claude":
			choice.SubmitText = ""
			if choice.Custom {
				choice.NavigateKey = strconv.Itoa(i + 1)
			} else {
				choice.DirectKey = strconv.Itoa(i + 1)
			}
		}
	}
	if agent == "opencode" {
		choices.ActiveIndex = -1
	}
	return choices
}

func addOpenCodeQuestionKeys(choices *parsedChoices) *parsedChoices {
	return decorateAgentChoices("opencode", choices)
}

func parseOpenCodePermission(output string) *parsedChoices {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	permissionLine := -1
	optionsLine := -1
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if optionsLine < 0 && strings.Contains(line, "Allow once") && strings.Contains(line, "Allow always") && strings.Contains(line, "Reject") {
			optionsLine = i
			continue
		}
		if optionsLine >= 0 && strings.Contains(strings.ToLower(line), "permission required") {
			permissionLine = i
			break
		}
	}
	if permissionLine < 0 || optionsLine < permissionLine || len(lines)-1-optionsLine > 4 {
		return nil
	}
	prompt := "Permission required"
	for i := permissionLine + 1; i < optionsLine; i++ {
		line := strings.TrimSpace(lines[i])
		if line != "" && !separatorPattern.MatchString(line) {
			prompt = line
			break
		}
	}
	return &parsedChoices{
		Prompt: prompt,
		Choices: []parsedChoice{
			{CleanText: "Allow once", DirectKey: "Enter"},
			{CleanText: "Reject", DirectKey: "Escape"},
		},
		ActiveIndex: -1,
	}
}

func bindChoicesToSource(choices *parsedChoices, output string) *parsedChoices {
	if choices == nil {
		return nil
	}
	sum := sha256.Sum256([]byte(output))
	choices.SourceFingerprint = hex.EncodeToString(sum[:])
	return choices
}

func parsePermissionChoices(output string) *parsedChoices {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	promptIndex := -1
	prompt := ""
	for i := len(lines) - 1; i >= 0; i-- {
		clean := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(lines[i]), "│┃ "))
		lower := strings.ToLower(clean)
		if strings.Contains(lower, "do you want to proceed?") ||
			strings.Contains(lower, "allow command?") ||
			strings.Contains(lower, "would you like to proceed?") ||
			strings.Contains(lower, "run a dynamic workflow?") ||
			strings.Contains(lower, "do you want to allow this connection?") ||
			(strings.Contains(lower, "do you want to") && strings.Contains(lower, "?")) ||
			(strings.Contains(lower, "would you like to") && strings.Contains(lower, "?")) {
			promptIndex = i
			prompt = clean
			break
		}
	}
	if promptIndex < 0 {
		return nil
	}

	var choices []parsedChoice
	activeIndex := -1
	for i := promptIndex + 1; i < len(lines); i++ {
		clean := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(lines[i]), "│┃ "))
		if clean == "" || separatorPattern.MatchString(clean) || isMenuFooterLine(clean) {
			continue
		}
		lower := strings.ToLower(clean)
		if strings.Contains(lower, "tab to amend") || strings.Contains(lower, "ctrl+e to explain") {
			continue
		}
		matches := permissionChoiceRe.FindStringSubmatch(clean)
		if matches == nil {
			if len(choices) > 0 {
				return nil
			}
			continue
		}
		if matches[1] != "" {
			activeIndex = len(choices)
		}
		choices = append(choices, parsedChoice{CleanText: strings.TrimSpace(matches[2])})
	}
	if len(choices) < 2 {
		return nil
	}
	return &parsedChoices{Prompt: prompt, Choices: choices, ActiveIndex: activeIndex}
}

func parseBinaryChoices(output string) *parsedChoices {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		lower := strings.ToLower(line)
		if !strings.Contains(lower, "[y/n]") && !strings.Contains(lower, "(y/n)") && !strings.Contains(lower, "yes (y)") {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) != "" {
				return nil
			}
		}
		return &parsedChoices{
			Prompt:      line,
			Choices:     []parsedChoice{{CleanText: "Yes", SubmitText: "y"}, {CleanText: "No", SubmitText: "n"}},
			ActiveIndex: -1,
		}
	}
	return nil
}

// parseChoices scans terminal output for a @clack/prompts style selection menu
// and returns the parsed choices, or nil if no selection prompt is found.
func parseChoices(output string) *parsedChoices {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")

	// --- 1. Find the help bar from the bottom ---
	helpIdx := -1
	for i := len(lines) - 1; i >= 0; i-- {
		stripped := strings.TrimSpace(lines[i])
		if helpBarPattern.MatchString(stripped) ||
			strings.HasPrefix(stripped, "Enter to") ||
			strings.HasPrefix(stripped, "Space to") {
			helpIdx = i
			break
		}
	}
	if helpIdx < 0 {
		return nil
	}
	helpLine := strings.ToLower(lines[helpIdx])
	if strings.Contains(helpLine, "submit answer") || strings.Contains(helpLine, "submit all") {
		return nil
	}
	for i := helpIdx + 1; i < len(lines); i++ {
		if !isMenuFooterLine(lines[i]) {
			return nil
		}
	}

	// --- 2. Find the prompt question above the help bar ---
	prompt := ""
	promptIdx := -1
	for i := helpIdx - 1; i >= 0; i-- {
		stripped := strings.TrimSpace(lines[i])
		if strings.HasPrefix(stripped, "?") || strings.HasSuffix(stripped, "?") {
			prompt = strings.TrimSpace(strings.TrimLeft(stripped, "│┃ "))
			promptIdx = i
			break
		}
	}
	if promptIdx < 0 {
		return nil
	}

	// --- 3. Collect choice lines between prompt and help ---
	// Clack aligns every option's text at the same column as the active option.
	// More-indented lines are descriptions and must never become selectable.
	type candidate struct {
		text       string
		textColumn int
		active     bool
		numbered   bool
	}
	var candidates []candidate
	activeColumn := -1
	for i := promptIdx + 1; i < helpIdx; i++ {
		raw := strings.TrimRight(lines[i], " \t\r")
		content := raw
		bordered := strings.TrimLeft(raw, " \t")
		if strings.HasPrefix(bordered, "│") || strings.HasPrefix(bordered, "┃") {
			content = strings.TrimPrefix(strings.TrimPrefix(bordered, "│"), "┃")
		}
		stripped := strings.TrimSpace(content)
		if stripped == "" {
			continue
		}
		if separatorPattern.MatchString(stripped) {
			continue
		}
		if strings.Contains(stripped, "Enter to") ||
			strings.Contains(stripped, "Space to") ||
			strings.Contains(stripped, "navigate") {
			continue
		}

		runes := []rune(content)
		first := 0
		for first < len(runes) && (runes[first] == ' ' || runes[first] == '\t') {
			first++
		}
		active := first < len(runes) && isActiveChoiceRune(runes[first])
		textColumn := first
		if active {
			textColumn++
			for textColumn < len(runes) && (runes[textColumn] == ' ' || runes[textColumn] == '\t') {
				textColumn++
			}
			activeColumn = textColumn
		}
		clean := strings.TrimSpace(choiceIndicators.ReplaceAllString(stripped, ""))
		numbered := false
		if matches := choiceLineRe.FindStringSubmatch(clean); matches != nil {
			clean = strings.TrimSpace(matches[2])
			numbered = true
		}
		if clean == "" {
			continue
		}
		candidates = append(candidates, candidate{text: clean, textColumn: textColumn, active: active, numbered: numbered})
	}

	var choices []parsedChoice
	activeIndex := -1
	allNumbered := true
	for _, item := range candidates {
		if activeColumn >= 0 && item.textColumn != activeColumn {
			continue
		}
		if item.active {
			activeIndex = len(choices)
		}
		if !item.numbered {
			allNumbered = false
		}
		choices = append(choices, parsedChoice{CleanText: item.text})
	}

	multiSelect := strings.Contains(strings.ToLower(lines[helpIdx]), "space")
	if len(choices) < 2 || (!multiSelect && activeIndex < 0 && !allNumbered) {
		return nil
	}

	return &parsedChoices{
		Prompt:      prompt,
		Choices:     choices,
		ActiveIndex: activeIndex,
		MultiSelect: multiSelect,
	}
}

// parseBoxChoices scans terminal output for a box-drawing style selection
// menu used by agents (opencode, Claude Code) and returns parsed choices.
// The format uses ┃ (U+2502) characters as a left border:
//
//	┃
//	┃  Question text
//	┃
//	┃  1. Choice A text
//	┃  2. Choice B text
//	┃  3. Type your own answer
//	┃
func parseBoxChoices(output string) *parsedChoices {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")

	// Restrict parsing to the final contiguous box region so old transcript
	// boxes and unrelated numbered output cannot become live actions.
	end := -1
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "│") || strings.HasPrefix(trimmed, "┃") {
			end = i
			break
		}
	}
	if end < 0 || len(lines)-1-end > 12 {
		return nil
	}
	start := end
	for i := end - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "│") || strings.HasPrefix(trimmed, "┃") || trimmed == "" {
			start = i
			continue
		}
		break
	}

	var contents []string
	for _, line := range lines[start : end+1] {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "│") && !strings.HasPrefix(trimmed, "┃") {
			continue
		}
		content := strings.TrimLeft(strings.TrimPrefix(strings.TrimPrefix(trimmed, "│"), "┃"), " ")
		contents = append(contents, content)
	}

	if len(contents) < 4 {
		return nil
	}

	// Find the question prompt: first non-empty, non-separator text
	// that does not start with a numbered choice pattern.
	prompt := ""
	promptIdx := -1
	for i, c := range contents {
		trimmed := strings.TrimSpace(c)
		if trimmed == "" {
			continue
		}
		if separatorPattern.MatchString(trimmed) {
			continue
		}
		if choiceLineRe.MatchString(trimmed) {
			continue
		}
		prompt = trimmed
		promptIdx = i
		break
	}
	if prompt == "" || promptIdx < 0 {
		return nil
	}

	// Collect numbered choice lines (N. text or N) text) after the prompt.
	var choices []parsedChoice
	for i := promptIdx + 1; i < len(contents); i++ {
		trimmed := strings.TrimSpace(contents[i])
		if trimmed == "" {
			continue
		}
		if separatorPattern.MatchString(trimmed) {
			continue
		}
		if matches := choiceLineRe.FindStringSubmatch(trimmed); matches != nil {
			choices = append(choices, parsedChoice{
				CleanText:  strings.TrimSpace(matches[2]),
				SubmitText: strings.TrimSpace(matches[2]),
			})
		}
	}

	if len(choices) == 0 {
		return nil
	}

	return &parsedChoices{
		Prompt:      prompt,
		Choices:     choices,
		ActiveIndex: -1,
	}
}

const (
	choiceCallbackPrefix       = "ch|"
	customChoiceCallbackPrefix = "cc|"
)

// buildChoiceKeyboard builds an inline keyboard from parsed choices.
// Each button's callback data is "ch|{nonce}|{index}" (1-based).
// Buttons include the visible choice text so Telegram users do not have to map
// a separate numbered list back to compact numeric controls.
func buildChoiceKeyboard(pc *parsedChoices, nonce string) *models.InlineKeyboardMarkup {
	if pc == nil || pc.MultiSelect || (pc.ActiveIndex < 0 && !hasDirectChoiceKeys(pc)) || len(pc.Choices) > 10 || nonce == "" {
		return nil
	}
	var rows [][]models.InlineKeyboardButton

	for i, choice := range pc.Choices {
		if choice.Skip || (pc.ActiveIndex < 0 && choice.DirectKey == "" && choice.NavigateKey == "") {
			continue
		}
		label := strconv.Itoa(i + 1)
		prefix := choiceCallbackPrefix
		if choice.Custom {
			prefix = customChoiceCallbackPrefix
		}
		btn := models.InlineKeyboardButton{
			Text:         truncateButtonLabel(label+" · "+choice.CleanText, 52),
			CallbackData: prefix + nonce + "|" + label,
		}
		rows = append(rows, []models.InlineKeyboardButton{btn})
	}

	return &models.InlineKeyboardMarkup{
		InlineKeyboard: rows,
	}
}

func hasDirectChoiceKeys(pc *parsedChoices) bool {
	if pc == nil || len(pc.Choices) == 0 {
		return false
	}
	for _, choice := range pc.Choices {
		if choice.DirectKey != "" || choice.NavigateKey != "" {
			return true
		}
	}
	return false
}
