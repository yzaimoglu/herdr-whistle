package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// commandHelp is the canonical command list, shared by /start and /help so the
// two cannot drift apart.
const commandHelp = `Commands:
/agents -- list all agents
/activity -- show recent agent activity
/notifications -- configure lifecycle notifications
/status <target> -- show agent status and explanation
/read <target> [N] -- read recent agent output (default 20, max 200 lines)
/send <target> <text> -- send text to an agent
/close <target> -- close an agent's pane
/startagent [<name> <kind> <pane> [-- <agent args...>]] -- open the wizard or start directly
/help -- show this message`

// formatAgentStatus builds an HTML status message for an agent target (agent
// name or pane ID). Falls back to raw JSON in a code block if parsing fails.
// Shared by the /status command and the inline 🔍 button so they stay
// consistent.
func formatAgentStatus(target string) (string, error) {
	getOut, err := herdrAgentGet(target)
	if err != nil {
		return "", err
	}
	return formatAgentFromGet(getOut), nil
}

// formatAgentFromGet renders the JSON from "herdr agent get" as a status
// message, falling back to the raw output in a code block if it doesn't parse.
func formatAgentFromGet(getOut string) string {
	var env agentGetEnvelope
	if json.Unmarshal([]byte(getOut), &env) == nil && env.Result.Agent.Agent != "" {
		a := env.Result.Agent
		return fmt.Sprintf(
			"<b>%s</b>\n\nStatus: %s\nPane: %s\nWorkspace: %s\nCwd: %s",
			escapeHTML(agentDisplayName(a)),
			a.AgentStatus,
			escapeHTML(a.PaneID),
			escapeHTML(a.WorkspaceID),
			escapeHTML(a.Cwd),
		)
	}
	return "<pre><code>" + escapeHTML(getOut) + "</code></pre>"
}

// ----- JSON types for herdr CLI responses -----

type agentListEnvelope struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
}

type agentListResult struct {
	Agents []agentInfo `json:"agents"`
}

type agentSession struct {
	Value string `json:"value"`
}

type agentInfo struct {
	Name           string       `json:"name"`
	Agent          string       `json:"agent"`
	AgentSession   agentSession `json:"agent_session"`
	AgentStatus    string       `json:"agent_status"`
	WorkspaceID    string       `json:"workspace_id"`
	PaneID         string       `json:"pane_id"`
	TerminalID     string       `json:"terminal_id"`
	Revision       uint64       `json:"revision"`
	StateChangeSeq uint64       `json:"state_change_seq"`
	Cwd            string       `json:"cwd"`
	Focused        bool         `json:"focused"`
	ForegroundCwd  string       `json:"foreground_cwd"`
}

func agentDisplayName(agent agentInfo) string {
	if agent.Name != "" {
		return agent.Name
	}
	return agent.Agent
}

func agentUILabel(agent agentInfo) string {
	if agent.Name != "" {
		return agent.Name
	}
	return fmt.Sprintf("%s · %s", agent.Agent, agent.PaneID)
}

func agentStatusPresentation(status string) (icon, label string) {
	switch strings.ToLower(status) {
	case "working", "running":
		return "⏳", "Working"
	case "blocked":
		return "⏸", "Needs input"
	case "done":
		return "✅", "Done"
	case "idle":
		return "💤", "Ready"
	default:
		return "❔", "Unknown"
	}
}

func validateAgentSnapshot(expected, current agentInfo) error {
	if current.TerminalID == "" || current.TerminalID != expected.TerminalID {
		return fmt.Errorf("agent terminal has changed")
	}
	if expected.Name != "" && current.Name != expected.Name {
		return fmt.Errorf("agent identity has changed")
	}
	if expected.AgentSession.Value != "" && current.AgentSession.Value != expected.AgentSession.Value {
		return fmt.Errorf("agent session has changed")
	}
	if expected.StateChangeSeq == 0 || current.StateChangeSeq != expected.StateChangeSeq {
		return fmt.Errorf("agent state generation has changed")
	}
	if current.Agent != expected.Agent {
		return fmt.Errorf("agent kind has changed")
	}
	return nil
}

func refreshAgentSnapshot(agent agentInfo) (agentInfo, error) {
	current, err := currentAgentByPane(agent.PaneID)
	if err != nil {
		return agentInfo{}, err
	}
	if err := validateAgentSnapshot(agent, current); err != nil {
		return agentInfo{}, err
	}
	return current, nil
}

func agentOperationKey(agent agentInfo) string {
	return "terminal:" + agent.TerminalID
}

func parseAgentFromGet(output string) (agentInfo, error) {
	var envelope agentGetEnvelope
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		return agentInfo{}, fmt.Errorf("parsing agent: %w", err)
	}
	if envelope.Result.Agent.PaneID == "" {
		return agentInfo{}, fmt.Errorf("agent response has no pane_id")
	}
	if envelope.Result.Agent.TerminalID == "" {
		return agentInfo{}, fmt.Errorf("agent response has no terminal_id")
	}
	return envelope.Result.Agent, nil
}

func getAgentInfo(target string) (agentInfo, error) {
	output, err := herdrAgentGet(target)
	if err != nil {
		return agentInfo{}, err
	}
	return parseAgentFromGet(output)
}

// agentGetEnvelope wraps the top-level herdr CLI response for agent get.
type agentGetEnvelope struct {
	ID     string               `json:"id"`
	Result agentGetNestedResult `json:"result"`
}

type agentGetNestedResult struct {
	Agent agentInfo `json:"agent"`
}

type agentReadEnvelope struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
}

type agentReadResult struct {
	Read agentReadContent `json:"read"`
}

type agentReadContent struct {
	Text   string `json:"text"`
	PaneID string `json:"pane_id"`
}

// ----------------------------------------------

var cfgGlobal *Config

func isAuthorized(userID, chatID int64) bool {
	return cfgGlobal != nil && userID == cfgGlobal.OwnerID && chatID == cfgGlobal.ChatID
}

// ownerAuth requires both the configured owner and that owner's private chat.
// Unauthorized updates are ignored so group chats cannot disclose bot output.
func ownerAuth(ctx context.Context, b *bot.Bot, update *models.Update) bool {
	if update.Message == nil || update.Message.From == nil {
		return false
	}
	return isAuthorized(update.Message.From.ID, update.Message.Chat.ID)
}

// sanitizeTTY strips terminal control characters while preserving prompt layout.
func sanitizeTTY(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// escapeHTML escapes HTML special characters for safe use in Telegram HTML mode.
func escapeHTML(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	)
	return replacer.Replace(s)
}

func escapeHTMLLimited(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	var result strings.Builder
	count := 0
	for _, r := range s {
		encoded := escapeHTML(string(r))
		encodedLen := len([]rune(encoded))
		if count+encodedLen > limit-1 {
			result.WriteRune('…')
			break
		}
		result.WriteString(encoded)
		count += encodedLen
	}
	return result.String()
}

// startHandler opens the main dashboard.
func startHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !ownerAuth(ctx, b, update) {
		return
	}
	sendAgentDashboard(ctx, b, update.Message.Chat.ID)
}

var (
	homeOnce sync.Once
	homeDir  string
)

// homeDirectory returns the cached user home directory (empty if it can't be
// resolved). Looked up once; shortenPath is called per agent per render.
func homeDirectory() string {
	homeOnce.Do(func() {
		if h, err := os.UserHomeDir(); err == nil {
			homeDir = h
		}
	})
	return homeDir
}

// shortenPath replaces the user's home directory with ~ for display.
func shortenPath(path string) string {
	return shortenPathIn(path, homeDirectory())
}

// shortenPathIn replaces home with ~. It matches on a path boundary so
// "/home/user" is not mistaken for "/home/user2". Pure for testing.
func shortenPathIn(path, home string) string {
	if home == "" {
		return path
	}
	if path == home || strings.HasPrefix(path, home+"/") {
		return "~" + path[len(home):]
	}
	return path
}

func truncateButtonLabel(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit-1]) + "…"
}

type listView struct {
	Filter string
	Page   int
	Mode   string
}

func defaultListView() listView {
	return listView{Filter: "all", Mode: currentPreferences().UIMode}
}

func buildAgentList() (string, *models.InlineKeyboardMarkup, error) {
	return buildAgentListView(defaultListView())
}

func buildAgentListView(view listView) (string, *models.InlineKeyboardMarkup, error) {
	raw, err := herdrAgentList()
	if err != nil {
		return "", nil, err
	}

	var envelope agentListEnvelope
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return "", nil, fmt.Errorf("parsing agent list JSON: %w", err)
	}

	var result agentListResult
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		return "", nil, fmt.Errorf("parsing agent list result: %w", err)
	}

	totalAgents := len(result.Agents)
	working := 0
	blocked := 0
	for _, agent := range result.Agents {
		switch strings.ToLower(agent.AgentStatus) {
		case "working", "running":
			working++
		case "blocked":
			blocked++
		}
	}
	var filtered []agentInfo
	for _, agent := range result.Agents {
		status := strings.ToLower(agent.AgentStatus)
		matches := view.Filter == "all" || view.Filter == ""
		switch view.Filter {
		case "working":
			matches = status == "working" || status == "running"
		case "blocked":
			matches = status == "blocked"
		case "ready":
			matches = status == "idle" || status == "done"
		}
		if matches {
			filtered = append(filtered, agent)
		}
	}
	if view.Filter == "" {
		view.Filter = "all"
	}
	if view.Mode != "compact" {
		view.Mode = "dashboard"
	}
	pageSize := 6
	if view.Mode == "compact" {
		pageSize = 10
	}
	totalPages := (len(filtered) + pageSize - 1) / pageSize
	if totalPages == 0 {
		totalPages = 1
	}
	if view.Page < 0 {
		view.Page = 0
	}
	if view.Page >= totalPages {
		view.Page = totalPages - 1
	}
	start := view.Page * pageSize
	end := start + pageSize
	if end > len(filtered) {
		end = len(filtered)
	}
	visibleAgents := filtered[start:end]

	var msg strings.Builder
	msg.WriteString("<b>Herdr control</b>\n")
	msg.WriteString(fmt.Sprintf("%d agents · %d working · %d need input\n", totalAgents, working, blocked))
	msg.WriteString(fmt.Sprintf("Filter: %s · Page %d/%d\n", escapeHTML(view.Filter), view.Page+1, totalPages))
	msg.WriteString("\nSelect an agent to inspect or control.")

	var rows [][]models.InlineKeyboardButton
	var compactRow []models.InlineKeyboardButton
	for _, a := range visibleAgents {
		displayName := agentUILabel(a)
		icon, label := agentStatusPresentation(a.AgentStatus)
		token, ok := agentUITokens.register(a)
		if !ok {
			continue
		}
		buttonText := fmt.Sprintf("%s %s · %s", icon, displayName, label)
		button := models.InlineKeyboardButton{
			Text:         truncateButtonLabel(buttonText, 52),
			CallbackData: "al|open|" + token,
		}
		if view.Mode == "compact" {
			button.Text = truncateButtonLabel(icon+" "+displayName, 25)
			compactRow = append(compactRow, button)
			if len(compactRow) == 2 {
				rows = append(rows, compactRow)
				compactRow = nil
			}
		} else {
			rows = append(rows, []models.InlineKeyboardButton{button})
		}
	}
	if len(compactRow) > 0 {
		rows = append(rows, compactRow)
	}

	rows = append(rows, []models.InlineKeyboardButton{
		{Text: "All", CallbackData: "al|list|all|0"},
		{Text: "Working", CallbackData: "al|list|working|0"},
		{Text: "Needs input", CallbackData: "al|list|blocked|0"},
		{Text: "Ready", CallbackData: "al|list|ready|0"},
	})
	if totalPages > 1 {
		prev := view.Page - 1
		if prev < 0 {
			prev = 0
		}
		next := view.Page + 1
		if next >= totalPages {
			next = totalPages - 1
		}
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: "Previous", CallbackData: fmt.Sprintf("al|list|%s|%d", view.Filter, prev)},
			{Text: fmt.Sprintf("%d/%d", view.Page+1, totalPages), CallbackData: "al|noop"},
			{Text: "Next", CallbackData: fmt.Sprintf("al|list|%s|%d", view.Filter, next)},
		})
	}
	modeLabel, modeValue := "Compact mode", "compact"
	if view.Mode == "compact" {
		modeLabel, modeValue = "Dashboard mode", "dashboard"
	}
	rows = append(rows, []models.InlineKeyboardButton{
		{Text: "Start agent", CallbackData: "al|start_wizard"},
		{Text: "Activity", CallbackData: "al|activity|0"},
	}, []models.InlineKeyboardButton{
		{Text: "Notifications", CallbackData: "al|prefs"},
		{Text: modeLabel, CallbackData: "al|mode|" + modeValue},
	}, []models.InlineKeyboardButton{
		{Text: "Refresh", CallbackData: fmt.Sprintf("al|list|%s|%d", view.Filter, view.Page)},
		{Text: "Help", CallbackData: "al|help"},
	})

	kb := &models.InlineKeyboardMarkup{InlineKeyboard: rows}
	return msg.String(), kb, nil
}

func sendAgentDashboard(ctx context.Context, b *bot.Bot, chatID int64) {
	sendAgentDashboardView(ctx, b, chatID, defaultListView())
}

func sendAgentDashboardView(ctx context.Context, b *bot.Bot, chatID int64, view listView) {
	stopTyping := startTyping(ctx, b, chatID)
	msg, keyboard, err := buildAgentListView(view)
	stopTyping()
	if err != nil {
		log.Printf("ERROR building agent dashboard: %v", err)
		sendText(ctx, b, chatID, "Could not load agents: "+err.Error())
		return
	}
	sendFormattedWithKeyboard(ctx, b, chatID, msg, keyboard)
}

func buildAgentDetail(agent agentInfo) (string, *models.InlineKeyboardMarkup, error) {
	token, ok := agentUITokens.register(agent)
	if !ok {
		return "", nil, fmt.Errorf("agent has no stable terminal identity")
	}
	icon, statusLabel := agentStatusPresentation(agent.AgentStatus)
	focus := ""
	if agent.Focused {
		focus = " · focused"
	}
	message := fmt.Sprintf(
		"%s <b>%s</b>\n%s%s\n\n<b>Agent</b>  <code>%s</code>\n<b>Pane</b>  <code>%s</code>\n<b>Workspace</b>  <code>%s</code>\n<b>Directory</b>  <code>%s</code>",
		icon,
		escapeHTML(agentUILabel(agent)),
		escapeHTML(statusLabel),
		focus,
		escapeHTML(agent.Agent),
		escapeHTML(agent.PaneID),
		escapeHTML(agent.WorkspaceID),
		escapeHTML(shortenPath(agent.Cwd)),
	)
	keyboard := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{
			{Text: "Send prompt", CallbackData: "al|prompt|" + token},
			{Text: "Read output", CallbackData: "al|output|" + token},
		},
		{
			{Text: "Refresh", CallbackData: "al|open|" + token},
			{Text: "Interrupt", CallbackData: "al|interrupt|" + token},
		},
		{{Text: "Close pane", CallbackData: "al|request_close|" + token}},
		{{Text: "Back to agents", CallbackData: "al|back"}},
	}}
	return message, keyboard, nil
}

func agentOpenKeyboard(agent agentInfo) *models.InlineKeyboardMarkup {
	token, ok := agentUITokens.register(agent)
	if !ok {
		return nil
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "Open agent", CallbackData: "al|open|" + token}},
	}}
}

func parseSendRequest(text string) (target, prompt string) {
	trimmed := strings.TrimSpace(text)
	commandEnd := strings.IndexFunc(trimmed, unicode.IsSpace)
	if commandEnd < 0 {
		return "", ""
	}
	rest := strings.TrimSpace(trimmed[commandEnd:])
	fields := strings.Fields(rest)
	if len(fields) < 2 {
		return "", ""
	}
	target = fields[0]
	targetAt := strings.Index(rest, target)
	prompt = strings.TrimSpace(rest[targetAt+len(target):])
	return target, prompt
}

func submitAgentPrompt(agent agentInfo, prompt string) (agentInfo, error) {
	prompt = strings.TrimSpace(sanitizeTTY(prompt))
	if prompt == "" {
		return agentInfo{}, fmt.Errorf("prompt is empty")
	}
	var submittedTo agentInfo
	_, err := paneOperations.run(agentOperationKey(agent), func() (string, error) {
		current, err := refreshAgentSnapshot(agent)
		if err != nil {
			return "", err
		}
		output, err := herdrAgentPrompt(current.PaneID, prompt)
		if err == nil {
			submittedTo = current
		}
		return output, err
	})
	return submittedTo, err
}

// agentsHandler sends the agent list as a formatted message with inline buttons.
func agentsHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !ownerAuth(ctx, b, update) {
		return
	}

	sendAgentDashboard(ctx, b, update.Message.Chat.ID)
}

// statusHandler shows agent status and explanation for a target.
func statusHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !ownerAuth(ctx, b, update) {
		return
	}

	args := parseCommandArgs(update.Message.Text)
	if len(args) < 2 {
		sendText(ctx, b, update.Message.Chat.ID, "Usage: /status <target>")
		return
	}
	target := args[1]
	stopTyping := startTyping(ctx, b, update.Message.Chat.ID)
	defer stopTyping()

	statusMsg, err := formatAgentStatus(target)
	if err != nil {
		log.Printf("ERROR getting agent %s: %v", target, err)
		sendText(ctx, b, update.Message.Chat.ID, "Error getting agent: "+err.Error())
		return
	}

	// Append the agent's explanation, best-effort. explain returns JSON, so it
	// is shown verbatim in a code block as supplementary context.
	var sb strings.Builder
	sb.WriteString(statusMsg)
	if explainOut, err := herdrAgentExplain(target); err == nil && strings.TrimSpace(explainOut) != "" {
		sb.WriteString("\n\n<pre><code>")
		sb.WriteString(escapeHTML(truncateText(explainOut, 2800)))
		sb.WriteString("</code></pre>")
	} else if err != nil {
		log.Printf("WARN explaining agent %s: %v", target, err)
	}

	sendFormatted(ctx, b, update.Message.Chat.ID, sb.String())
}

// readHandler reads recent agent output.
func readHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !ownerAuth(ctx, b, update) {
		return
	}

	args := parseCommandArgs(update.Message.Text)
	if len(args) < 2 {
		sendText(ctx, b, update.Message.Chat.ID, "Usage: /read <target> [N]")
		return
	}
	target := args[1]

	lines := 20
	if len(args) >= 3 {
		n, err := strconv.Atoi(args[2])
		if err == nil && n > 0 {
			lines = n
		}
	}
	if lines > 200 {
		lines = 200
	}

	stopTyping := startTyping(ctx, b, update.Message.Chat.ID)
	defer stopTyping()
	out, err := herdrAgentRead(target, lines)
	if err != nil {
		log.Printf("ERROR reading agent %s: %v", target, err)
		sendText(ctx, b, update.Message.Chat.ID, "Error reading agent: "+err.Error())
		return
	}

	text := truncateText(normalizeAgentReadOutput(out), 3500)
	formatted := "<pre><code>" + escapeHTML(text) + "</code></pre>"
	sendFormatted(ctx, b, update.Message.Chat.ID, formatted)
}

// sendHandler sends text to an agent.
func sendHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !ownerAuth(ctx, b, update) {
		return
	}

	target, text := parseSendRequest(update.Message.Text)
	if target == "" || text == "" {
		sendText(ctx, b, update.Message.Chat.ID, "Usage: /send <target> <text>")
		return
	}

	stopTyping := startTyping(ctx, b, update.Message.Chat.ID)
	defer stopTyping()
	agent, err := getAgentInfo(target)
	if err != nil {
		log.Printf("ERROR getting pane for agent %s: %v", target, err)
		sendText(ctx, b, update.Message.Chat.ID, "Error resolving agent: "+err.Error())
		return
	}
	submittedTo, err := submitAgentPrompt(agent, text)
	if err != nil {
		log.Printf("ERROR sending to agent %s: %v", target, err)
		sendText(ctx, b, update.Message.Chat.ID, "Error sending to agent: "+err.Error())
		return
	}

	reply := fmt.Sprintf("✅ <b>Prompt sent to %s</b>", escapeHTML(agentUILabel(submittedTo)))
	recordAgentActivity("prompt", submittedTo)
	sendFormattedWithKeyboard(ctx, b, update.Message.Chat.ID, reply, agentOpenKeyboard(submittedTo))
}

// closeHandler closes an agent's pane.
func closeHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !ownerAuth(ctx, b, update) {
		return
	}

	args := parseCommandArgs(update.Message.Text)
	if len(args) < 2 {
		sendText(ctx, b, update.Message.Chat.ID, "Usage: /close <target>")
		return
	}
	target := args[1]

	agent, err := getAgentInfo(target)
	if err != nil {
		log.Printf("ERROR getting pane for agent %s: %v", target, err)
		sendText(ctx, b, update.Message.Chat.ID, "Error getting pane: "+err.Error())
		return
	}

	token, ok := agentUITokens.register(agent)
	if !ok {
		sendText(ctx, b, update.Message.Chat.ID, "Could not create a close confirmation.")
		return
	}
	handleConfirmationRequest(ctx, b, update.Message.Chat.ID, "close", token)
}

// parseStartAgentArgs parses Herdr's current agent-start contract. A standalone
// "--" after the required name, kind, and pane separates optional agent args.
func parseStartAgentArgs(rest string) (name, kind, paneID string, agentArgs []string) {
	fields := strings.Fields(rest)
	if len(fields) < 3 {
		return "", "", "", nil
	}
	name = fields[0]
	kind = fields[1]
	paneID = fields[2]
	for i := 3; i < len(fields); i++ {
		if fields[i] == "--" {
			agentArgs = append(agentArgs, fields[i+1:]...)
			break
		}
		agentArgs = append(agentArgs, fields[i])
	}
	return name, kind, paneID, agentArgs
}

// startAgentHandler starts a new agent with a command.
func startAgentHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !ownerAuth(ctx, b, update) {
		return
	}

	text := strings.TrimSpace(update.Message.Text)
	// Remove "/startagent" prefix
	rest := strings.TrimPrefix(text, "/startagent")
	rest = strings.TrimSpace(rest)

	if rest == "" {
		handleStartAgentWizard(ctx, b, update.Message.Chat.ID, 0, 0)
		return
	}

	name, kind, paneID, agentArgs := parseStartAgentArgs(rest)
	if name == "" {
		sendText(ctx, b, update.Message.Chat.ID, "Usage: /startagent <name> <kind> <pane> [-- <agent args...>]")
		return
	}

	stopTyping := startTyping(ctx, b, update.Message.Chat.ID)
	defer stopTyping()
	terminalID, err := herdrPaneTerminalID(paneID)
	if err != nil {
		log.Printf("ERROR resolving terminal for pane %s: %v", paneID, err)
		sendText(ctx, b, update.Message.Chat.ID, "Error resolving pane: "+err.Error())
		return
	}
	out, err := paneOperations.run("terminal:"+terminalID, func() (string, error) {
		return herdrAgentStart(name, kind, paneID, agentArgs...)
	})
	if err != nil {
		log.Printf("ERROR starting agent %s: %v", name, err)
		sendText(ctx, b, update.Message.Chat.ID, "Error starting agent: "+err.Error())
		return
	}

	reply := fmt.Sprintf("Started agent %s:\n%s", escapeHTML(name), escapeHTML(out))
	recordAgentActivity("started", agentInfo{Name: name, Agent: kind, AgentStatus: "working", PaneID: paneID, TerminalID: terminalID})
	sendFormatted(ctx, b, update.Message.Chat.ID, reply)
}

// helpHandler shows available commands.
func helpHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !ownerAuth(ctx, b, update) {
		return
	}
	message, keyboard := helpCard()
	sendFormattedWithKeyboard(ctx, b, update.Message.Chat.ID, message, keyboard)
}

// ----- Inline keyboard callback handler -----

const (
	cbPrefix              = "al|" // agent list callbacks start with "al|"
	blockedResponsePrefix = "br|"
)

// callbackChatInfo extracts chatID and msgID from a callback query's
// MaybeInaccessibleMessage union. Returns false if neither branch is set.
func callbackChatInfo(update *models.Update) (chatID int64, msgID int, ok bool) {
	if msg := update.CallbackQuery.Message.Message; msg != nil {
		return msg.Chat.ID, msg.ID, true
	} else if im := update.CallbackQuery.Message.InaccessibleMessage; im != nil {
		return im.Chat.ID, im.MessageID, true
	}
	return 0, 0, false
}

// Callback data: ch|{one-use nonce}|{index} (1-based).

// choiceKeys builds the key sequence from the verified zero-based cursor
// position to the selected one-based option.
func choiceKeys(activeIndex, selectedIndex int) []string {
	delta := selectedIndex - 1 - activeIndex
	keys := make([]string, 0, abs(delta)+1)
	key := "Down"
	if delta < 0 {
		key = "Up"
		delta = -delta
	}
	for j := 0; j < delta; j++ {
		keys = append(keys, key)
	}
	keys = append(keys, "Enter")
	return keys
}

func deliverCursorChoice(
	initial *parsedChoices,
	selectedIndex int,
	sendKey func(string) error,
	readChoices func() *parsedChoices,
	validate func() error,
	pause func(),
) error {
	if initial == nil || initial.ActiveIndex < 0 || selectedIndex < 1 || selectedIndex > len(initial.Choices) {
		return fmt.Errorf("invalid cursor selection")
	}
	menuFingerprint := choiceMenuFingerprint(initial)
	cursor := initial.ActiveIndex
	target := selectedIndex - 1
	for cursor != target {
		key := "Down"
		expected := cursor + 1
		if target < cursor {
			key = "Up"
			expected = cursor - 1
		}
		if err := validate(); err != nil {
			return err
		}
		if err := sendKey(key); err != nil {
			return err
		}
		moved := false
		for attempt := 0; attempt < 20; attempt++ {
			pause()
			current := readChoices()
			if current == nil || choiceMenuFingerprint(current) != menuFingerprint {
				continue
			}
			if current.ActiveIndex == expected {
				cursor = expected
				moved = true
				break
			}
		}
		if !moved {
			return fmt.Errorf("choice cursor did not move to option %d", expected+1)
		}
	}
	if err := validate(); err != nil {
		return err
	}
	return sendKey("Enter")
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func currentAgentByPane(paneID string) (agentInfo, error) {
	out, err := herdrAgentList()
	if err != nil {
		return agentInfo{}, err
	}
	var envelope agentListEnvelope
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		return agentInfo{}, fmt.Errorf("parsing agent list: %w", err)
	}
	var result agentListResult
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		return agentInfo{}, fmt.Errorf("parsing agent list result: %w", err)
	}
	for _, agent := range result.Agents {
		if agent.PaneID == paneID {
			return agent, nil
		}
	}
	return agentInfo{}, fmt.Errorf("pane %s no longer has an agent", paneID)
}

func choiceCallbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.CallbackQuery == nil {
		return
	}
	data := update.CallbackQuery.Data
	if !strings.HasPrefix(data, choiceCallbackPrefix) {
		return
	}

	chatID, msgID, ok := callbackChatInfo(update)
	if !ok {
		return
	}
	userID := update.CallbackQuery.From.ID

	if !isAuthorized(userID, chatID) {
		b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: update.CallbackQuery.ID,
			Text:            "Unauthorized",
			ShowAlert:       true,
		})
		return
	}

	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: update.CallbackQuery.ID,
	})

	trimmed := strings.TrimPrefix(data, choiceCallbackPrefix)
	parts := strings.SplitN(trimmed, "|", 2)
	if len(parts) < 2 {
		return
	}
	nonce := parts[0]
	choiceIndex := parts[1]

	idx, err := strconv.Atoi(choiceIndex)
	if err != nil || idx < 1 {
		sendText(ctx, b, chatID, "Invalid choice. Use Send response or reopen the agent.")
		return
	}
	pending, ok := pendingChoices.claim(nonce)
	if !ok || idx > pending.ChoiceCount {
		sendText(ctx, b, chatID, "This choice expired or was already used. The other notification actions remain available.")
		return
	}

	_, err = paneOperations.run("terminal:"+pending.TerminalID, func() (string, error) {
		validate := func() error {
			agent, err := currentAgentByPane(pending.PaneID)
			if err != nil {
				return err
			}
			if strings.ToLower(agent.AgentStatus) != "blocked" {
				return fmt.Errorf("agent is no longer blocked")
			}
			if pending.SessionID != "" && agent.AgentSession.Value != pending.SessionID {
				return fmt.Errorf("agent session has changed")
			}
			if pending.StateSeq == 0 || agent.StateChangeSeq != pending.StateSeq {
				return fmt.Errorf("agent state generation has changed")
			}
			if pending.AgentName != "" && agent.Name != pending.AgentName {
				return fmt.Errorf("agent identity has changed")
			}
			if agent.Agent != pending.AgentKind {
				return fmt.Errorf("agent kind has changed")
			}
			if agent.TerminalID == "" || agent.TerminalID != pending.TerminalID {
				return fmt.Errorf("agent terminal has changed")
			}
			return nil
		}
		if err := validate(); err != nil {
			return "", err
		}
		currentText := readPaneText(pending.PaneID)
		current := parseAgentChoicesFor(pending.AgentKind, currentText)
		if current == nil || current.MultiSelect || choiceFingerprint(current) != pending.Fingerprint {
			return "", fmt.Errorf("prompt or cursor has changed")
		}
		if idx > len(current.Choices) {
			return "", fmt.Errorf("choice is no longer available")
		}
		sendKey := func(key string) error {
			_, err := herdrAgentSendKeys(pending.PaneID, key)
			return err
		}
		if key := current.Choices[idx-1].DirectKey; key != "" {
			if err := validate(); err != nil {
				return "", err
			}
			return "", sendKey(key)
		}
		if current.ActiveIndex < 0 {
			return "", fmt.Errorf("choice cursor is not visible")
		}
		err := deliverCursorChoice(
			current,
			idx,
			sendKey,
			func() *parsedChoices { return parseAgentChoicesFor(pending.AgentKind, readPaneText(pending.PaneID)) },
			validate,
			func() { time.Sleep(100 * time.Millisecond) },
		)
		return "", err
	})
	if err != nil {
		log.Printf("ERROR sending choice %s to pane %s: %v", choiceIndex, pending.PaneID, err)
		agent := agentInfo{
			Name: pending.AgentName, Agent: pending.AgentKind, AgentStatus: "blocked",
			PaneID: pending.PaneID, TerminalID: pending.TerminalID,
			StateChangeSeq: pending.StateSeq, AgentSession: agentSession{Value: pending.SessionID},
		}
		message := fmt.Sprintf("Choice not sent: %s\n\nUse <b>Send response</b> or reopen the agent.", escapeHTML(err.Error()))
		editFormattedWithKeyboard(ctx, b, chatID, msgID, message, blockedActionKeyboard(agent, nil))
		return
	}

	editMessageText(ctx, b, chatID, msgID,
		fmt.Sprintf("Sent choice <b>%s</b> to <b>%s</b>.", escapeHTML(choiceIndex), escapeHTML(pending.PaneID)))
	recordAgentActivity("response", agentInfo{Name: pending.AgentName, Agent: pending.AgentKind, PaneID: pending.PaneID, TerminalID: pending.TerminalID})
}

func blockedResponseCallbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.CallbackQuery == nil || !strings.HasPrefix(update.CallbackQuery.Data, blockedResponsePrefix) {
		return
	}
	chatID, msgID, ok := callbackChatInfo(update)
	if !ok {
		return
	}
	if !isAuthorized(update.CallbackQuery.From.ID, chatID) {
		b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: update.CallbackQuery.ID,
			Text:            "Unauthorized",
			ShowAlert:       true,
		})
		return
	}
	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: update.CallbackQuery.ID})
	token := strings.TrimPrefix(update.CallbackQuery.Data, blockedResponsePrefix)
	response, ok := pendingBlockedResponses.claim(token)
	if !ok {
		sendText(ctx, b, chatID, "This option expired or was already used. The other notification actions remain available.")
		return
	}
	var submittedTo agentInfo
	_, err := paneOperations.run(agentOperationKey(response.Agent), func() (string, error) {
		current, err := refreshAgentSnapshot(response.Agent)
		if err != nil {
			return "", err
		}
		if strings.ToLower(current.AgentStatus) != "blocked" {
			return "", fmt.Errorf("agent is no longer blocked")
		}
		visible := readPaneText(current.PaneID)
		choices := parseAgentChoicesFor(current.Agent, visible)
		if choices == nil || choiceFingerprint(choices) != response.Fingerprint {
			return "", fmt.Errorf("blocked prompt has changed")
		}
		latest, err := refreshAgentSnapshot(current)
		if err != nil || latest.StateChangeSeq != current.StateChangeSeq {
			return "", fmt.Errorf("agent changed while validating response")
		}
		output, err := herdrAgentPrompt(latest.PaneID, response.Text)
		if err == nil {
			submittedTo = latest
		}
		return output, err
	})
	if err != nil {
		message := "Response was not sent: " + escapeHTML(err.Error()) + "\n\nUse <b>Send response</b> or reopen the agent."
		editFormattedWithKeyboard(ctx, b, chatID, msgID, message, blockedActionKeyboard(response.Agent, nil))
		return
	}
	message := fmt.Sprintf("✅ Sent <b>%s</b> to <b>%s</b>.", escapeHTMLLimited(response.Text, 180), escapeHTML(agentUILabel(submittedTo)))
	recordAgentActivity("response", submittedTo)
	editMessageText(ctx, b, chatID, msgID, message)
}

// agentsCallbackHandler processes button presses on the agent list inline keyboard.
func agentsCallbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.CallbackQuery == nil {
		return
	}
	data := update.CallbackQuery.Data
	if !strings.HasPrefix(data, cbPrefix) {
		return
	}

	chatID, msgID, ok := callbackChatInfo(update)
	if !ok {
		return
	}
	userID := update.CallbackQuery.From.ID

	// Only the owner can interact with the buttons.
	if !isAuthorized(userID, chatID) {
		b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
			CallbackQueryID: update.CallbackQuery.ID,
			Text:            "Unauthorized",
			ShowAlert:       true,
		})
		return
	}

	// Acknowledge the callback immediately to dismiss the loading spinner.
	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{
		CallbackQueryID: update.CallbackQuery.ID,
	})

	// Parse: al|action|payload.
	trimmed := strings.TrimPrefix(data, cbPrefix)
	parts := strings.SplitN(trimmed, "|", 2)
	action := parts[0]

	var paneID string
	if len(parts) > 1 {
		paneID = parts[1]
	}

	switch action {
	case "refresh":
		handleRefresh(ctx, b, chatID, msgID)
	case "back":
		handleRefresh(ctx, b, chatID, msgID)
	case "list":
		handleListView(ctx, b, chatID, msgID, paneID)
	case "activity":
		handleActivityCard(ctx, b, chatID, msgID, paneID)
	case "prefs":
		handlePreferencesCard(ctx, b, chatID, msgID)
	case "toggle_pref":
		handlePreferenceToggle(ctx, b, chatID, msgID, paneID)
	case "mode":
		handleModeChange(ctx, b, chatID, msgID, paneID)
	case "start_wizard":
		handleStartAgentWizard(ctx, b, chatID, msgID, 0)
	case "noop":
		return
	case "help":
		handleHelpCard(ctx, b, chatID, msgID)
	case "open":
		handleAgentOpen(ctx, b, chatID, msgID, paneID)
	case "prompt":
		handlePromptRequest(ctx, b, chatID, userID, paneID)
	case "output":
		handleAgentOutput(ctx, b, chatID, paneID)
	case "interrupt":
		handleConfirmationRequest(ctx, b, chatID, "interrupt", paneID)
	case "request_close":
		handleConfirmationRequest(ctx, b, chatID, "close", paneID)
	case "confirm_interrupt":
		handleInterruptExec(ctx, b, chatID, msgID, paneID)
	case "confirm_close":
		handleCloseExec(ctx, b, chatID, msgID, paneID)
	case "cancel":
		confirmationTokens.get(paneID, true)
		editMessageText(ctx, b, chatID, msgID, "Cancelled.")
	case "status":
		handleAgentStatus(ctx, b, chatID, paneID)
	case "read":
		handleAgentRead(ctx, b, chatID, paneID)
	case "close":
		handleLegacyCloseRequest(ctx, b, chatID, paneID)
	case "close_confirm":
		editMessageText(ctx, b, chatID, msgID, "This old confirmation expired. Open the agent and try again.")
	case "close_cancel":
		editMessageText(ctx, b, chatID, msgID, "Cancelled.")
	}
}

func editFormattedWithKeyboard(ctx context.Context, b *bot.Bot, chatID int64, msgID int, text string, keyboard *models.InlineKeyboardMarkup) {
	var replyMarkup interface{}
	if keyboard != nil {
		replyMarkup = keyboard
	}
	if _, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:      chatID,
		MessageID:   msgID,
		Text:        text,
		ParseMode:   models.ParseModeHTML,
		ReplyMarkup: replyMarkup,
	}); err != nil {
		log.Printf("ERROR editing UI message %d in chat %d: %v", msgID, chatID, err)
	}
}

func expiredActionCard(ctx context.Context, b *bot.Bot, chatID int64, msgID int) {
	keyboard := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "Refresh agents", CallbackData: "al|back"}},
	}}
	editFormattedWithKeyboard(ctx, b, chatID, msgID, "This agent view expired or the agent session changed. Refresh to continue safely.", keyboard)
}

func resolveUIAgent(token string) (agentInfo, bool) {
	snapshot, ok := agentUITokens.get(token, false)
	if !ok {
		return agentInfo{}, false
	}
	current, err := refreshAgentSnapshot(snapshot)
	if err == nil {
		return current, true
	}

	// Status transitions invalidate state_change_seq. Recover the view only if
	// the durable agent identity still matches the original button.
	current, err = currentAgentByPane(snapshot.PaneID)
	if err != nil || current.TerminalID != snapshot.TerminalID || current.Agent != snapshot.Agent {
		return agentInfo{}, false
	}
	if snapshot.Name != "" && current.Name != snapshot.Name {
		return agentInfo{}, false
	}
	if snapshot.AgentSession.Value != "" && current.AgentSession.Value != snapshot.AgentSession.Value {
		return agentInfo{}, false
	}
	return current, true
}

func handleAgentOpen(ctx context.Context, b *bot.Bot, chatID int64, msgID int, token string) {
	agent, ok := resolveUIAgent(token)
	if !ok {
		expiredActionCard(ctx, b, chatID, msgID)
		return
	}
	message, keyboard, err := buildAgentDetail(agent)
	if err != nil {
		expiredActionCard(ctx, b, chatID, msgID)
		return
	}
	editFormattedWithKeyboard(ctx, b, chatID, msgID, message, keyboard)
}

func handleHelpCard(ctx context.Context, b *bot.Bot, chatID int64, msgID int) {
	message, keyboard := helpCard()
	editFormattedWithKeyboard(ctx, b, chatID, msgID, message, keyboard)
}

func helpCard() (string, *models.InlineKeyboardMarkup) {
	message := "<b>Herdr Whistle</b>\n\nUse the buttons for everyday work. Commands remain available for direct access:\n\n<pre><code>" + escapeHTML(commandHelp) + "</code></pre>"
	keyboard := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "Back to agents", CallbackData: "al|back"}},
	}}
	return message, keyboard
}

func handlePromptRequest(ctx context.Context, b *bot.Bot, chatID, userID int64, token string) {
	agent, ok := resolveUIAgent(token)
	if !ok {
		sendText(ctx, b, chatID, "This agent action expired. Open the agent again.")
		return
	}
	message := fmt.Sprintf("Reply to this message with a prompt for <b>%s</b>.\n\nThe request expires in 10 minutes.", escapeHTML(agentUILabel(agent)))
	sent, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      message,
		ParseMode: models.ParseModeHTML,
		ReplyMarkup: &models.ForceReply{
			ForceReply:            true,
			InputFieldPlaceholder: "Type a prompt…",
			Selective:             true,
		},
	})
	if err != nil {
		log.Printf("ERROR requesting prompt reply: %v", err)
		return
	}
	pendingPrompts.register(chatID, sent.ID, userID, agent)
}

func handleAgentOutput(ctx context.Context, b *bot.Bot, chatID int64, token string) {
	stopTyping := startTyping(ctx, b, chatID)
	defer stopTyping()
	agent, ok := resolveUIAgent(token)
	if !ok {
		sendText(ctx, b, chatID, "This agent action expired. Open the agent again.")
		return
	}
	out, err := herdrAgentRead(agent.PaneID, 30)
	if err != nil {
		sendText(ctx, b, chatID, "Could not read agent output: "+err.Error())
		return
	}
	text := truncateText(normalizeAgentReadOutput(out), 3300)
	message := fmt.Sprintf("<b>%s · recent output</b>\n<pre><code>%s</code></pre>", escapeHTML(agentUILabel(agent)), escapeHTML(text))
	sendFormattedWithKeyboard(ctx, b, chatID, message, agentOpenKeyboard(agent))
}

func handleConfirmationRequest(ctx context.Context, b *bot.Bot, chatID int64, action, token string) {
	agent, ok := resolveUIAgent(token)
	if !ok {
		sendText(ctx, b, chatID, "This agent action expired. Open the agent again.")
		return
	}
	confirmationToken, ok := confirmationTokens.register(agent)
	if !ok {
		sendText(ctx, b, chatID, "Could not create confirmation.")
		return
	}
	verb := "interrupt"
	confirmAction := "confirm_interrupt"
	if action == "close" {
		verb = "close the pane for"
		confirmAction = "confirm_close"
	}
	message := fmt.Sprintf("Confirm: %s <b>%s</b>?\nPane: <code>%s</code>", verb, escapeHTML(agentUILabel(agent)), escapeHTML(agent.PaneID))
	keyboard := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: "Confirm", CallbackData: "al|" + confirmAction + "|" + confirmationToken},
		{Text: "Cancel", CallbackData: "al|cancel|" + confirmationToken},
	}}}
	sendFormattedWithKeyboard(ctx, b, chatID, message, keyboard)
}

func handleLegacyCloseRequest(ctx context.Context, b *bot.Bot, chatID int64, paneID string) {
	agent, err := currentAgentByPane(paneID)
	if err != nil {
		sendText(ctx, b, chatID, "Agent is no longer available.")
		return
	}
	token, ok := agentUITokens.register(agent)
	if !ok {
		sendText(ctx, b, chatID, "Could not create a close confirmation.")
		return
	}
	handleConfirmationRequest(ctx, b, chatID, "close", token)
}

func handleInterruptExec(ctx context.Context, b *bot.Bot, chatID int64, msgID int, token string) {
	agent, ok := confirmationTokens.get(token, true)
	if !ok {
		editMessageText(ctx, b, chatID, msgID, "This confirmation expired.")
		return
	}
	_, err := paneOperations.run(agentOperationKey(agent), func() (string, error) {
		current, err := refreshAgentSnapshot(agent)
		if err != nil {
			return "", err
		}
		return herdrAgentSendKeys(current.PaneID, "ctrl+c")
	})
	if err != nil {
		editMessageText(ctx, b, chatID, msgID, "Interrupt failed: "+escapeHTML(err.Error()))
		return
	}
	recordAgentActivity("interrupted", agent)
	editMessageText(ctx, b, chatID, msgID, "Agent interrupted.")
}

func handleCloseExec(ctx context.Context, b *bot.Bot, chatID int64, msgID int, token string) {
	agent, ok := confirmationTokens.get(token, true)
	if !ok {
		editMessageText(ctx, b, chatID, msgID, "This confirmation expired.")
		return
	}
	_, err := paneOperations.run(agentOperationKey(agent), func() (string, error) {
		current, err := refreshAgentSnapshot(agent)
		if err != nil {
			return "", err
		}
		return herdrPaneClose(current.PaneID)
	})
	if err != nil {
		editMessageText(ctx, b, chatID, msgID, "Close failed: "+escapeHTML(err.Error()))
		return
	}
	recordAgentActivity("closed", agent)
	editMessageText(ctx, b, chatID, msgID, "Pane closed.")
}

func handleRefresh(ctx context.Context, b *bot.Bot, chatID int64, msgID int) {
	stopTyping := startTyping(ctx, b, chatID)
	msg, kb, err := buildAgentList()
	stopTyping()
	if err != nil {
		log.Printf("ERROR rebuilding agent list: %v", err)
		editMessageText(ctx, b, chatID, msgID, "Error refreshing: "+escapeHTML(err.Error()))
		return
	}

	editFormattedWithKeyboard(ctx, b, chatID, msgID, msg, kb)
}

func handleAgentStatus(ctx context.Context, b *bot.Bot, chatID int64, target string) {
	if target == "" {
		return
	}
	msg, err := formatAgentStatus(target)
	if err != nil {
		sendText(ctx, b, chatID, "Error getting agent: "+err.Error())
		return
	}
	sendFormatted(ctx, b, chatID, msg)
}

func handleAgentRead(ctx context.Context, b *bot.Bot, chatID int64, target string) {
	if target == "" {
		return
	}

	out, err := herdrAgentRead(target, 20)
	if err != nil {
		sendText(ctx, b, chatID, "Error reading agent: "+err.Error())
		return
	}

	text := truncateText(normalizeAgentReadOutput(out), 3400)
	msg := fmt.Sprintf("<b>Output from %s:</b>\n<pre><code>%s</code></pre>",
		escapeHTML(target), escapeHTML(text))
	sendFormatted(ctx, b, chatID, msg)
}

// editMessageText is a helper that edits a message's text (HTML).
func editMessageText(ctx context.Context, b *bot.Bot, chatID int64, msgID int, text string) {
	if _, err := b.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: msgID,
		Text:      text,
		ParseMode: models.ParseModeHTML,
	}); err != nil {
		log.Printf("ERROR editing message %d in chat %d: %v", msgID, chatID, err)
	}
}

// ---------------------------------------------

func pendingPromptReplyMatch(update *models.Update) bool {
	if update.Message == nil || update.Message.From == nil || update.Message.ReplyToMessage == nil {
		return false
	}
	if strings.TrimSpace(update.Message.Text) == "" {
		return false
	}
	return pendingPrompts.has(update.Message.Chat.ID, update.Message.ReplyToMessage.ID, update.Message.From.ID)
}

func promptReplyHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.Message == nil || update.Message.From == nil || !isAuthorized(update.Message.From.ID, update.Message.Chat.ID) {
		return
	}
	handlePendingPromptReply(ctx, b, update)
}

func handlePendingPromptReply(ctx context.Context, b *bot.Bot, update *models.Update) bool {
	if !pendingPromptReplyMatch(update) {
		return false
	}
	agent, ok := pendingPrompts.claim(update.Message.Chat.ID, update.Message.ReplyToMessage.ID, update.Message.From.ID)
	if !ok {
		return false
	}
	submittedTo, err := submitAgentPrompt(agent, update.Message.Text)
	if err != nil {
		sendText(ctx, b, update.Message.Chat.ID, "Prompt was not sent: "+err.Error())
		return true
	}
	recordAgentActivity("prompt", submittedTo)
	message := fmt.Sprintf("✅ <b>Prompt sent to %s</b>", escapeHTML(agentUILabel(submittedTo)))
	sendFormattedWithKeyboard(ctx, b, update.Message.Chat.ID, message, agentOpenKeyboard(submittedTo))
	return true
}

// defaultHandler handles authorized messages that are not commands or replies.
func defaultHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.Message == nil || update.Message.From == nil {
		return
	}
	if !isAuthorized(update.Message.From.ID, update.Message.Chat.ID) {
		return
	}
	if handlePendingPromptReply(ctx, b, update) {
		return
	}
	keyboard := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: "Open agents", CallbackData: "al|refresh"},
		{Text: "Help", CallbackData: "al|help"},
	}}}
	sendFormattedWithKeyboard(ctx, b, update.Message.Chat.ID, "I did not recognize that message. Open an agent or use <code>/help</code>.", keyboard)
}

// parseCommandArgs splits a message text into whitespace-separated tokens.
func parseCommandArgs(text string) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return strings.Fields(text)
}
