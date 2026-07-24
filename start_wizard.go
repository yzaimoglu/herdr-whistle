package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

const startAgentPrefix = "sa|"

var agentNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type paneInfo struct {
	PaneID        string `json:"pane_id"`
	TerminalID    string `json:"terminal_id"`
	WorkspaceID   string `json:"workspace_id"`
	Cwd           string `json:"cwd"`
	TerminalTitle string `json:"terminal_title_stripped"`
}

type startPaneEntry struct {
	Pane      paneInfo
	ExpiresAt time.Time
}

type pendingStart struct {
	OwnerID   int64
	Pane      paneInfo
	Kind      string
	ExpiresAt time.Time
}

var startWizardState = struct {
	sync.Mutex
	panes   map[string]startPaneEntry
	replies map[pendingPromptKey]pendingStart
}{panes: make(map[string]startPaneEntry), replies: make(map[pendingPromptKey]pendingStart)}

func listPanes() ([]paneInfo, error) {
	raw, err := herdrPaneList()
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Result struct {
			Panes []paneInfo `json:"panes"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return nil, fmt.Errorf("parsing pane list: %w", err)
	}
	return envelope.Result.Panes, nil
}

func registerStartPane(pane paneInfo) (string, bool) {
	token, ok := randomUIToken()
	if !ok || pane.PaneID == "" || pane.TerminalID == "" {
		return "", false
	}
	now := time.Now()
	startWizardState.Lock()
	defer startWizardState.Unlock()
	for key, entry := range startWizardState.panes {
		if now.After(entry.ExpiresAt) {
			delete(startWizardState.panes, key)
		}
	}
	startWizardState.panes[token] = startPaneEntry{Pane: pane, ExpiresAt: now.Add(10 * time.Minute)}
	return token, true
}

func getStartPane(token string, consume bool) (paneInfo, bool) {
	now := time.Now()
	startWizardState.Lock()
	defer startWizardState.Unlock()
	entry, ok := startWizardState.panes[token]
	if !ok || now.After(entry.ExpiresAt) {
		delete(startWizardState.panes, token)
		return paneInfo{}, false
	}
	if consume {
		delete(startWizardState.panes, token)
	}
	return entry.Pane, true
}

func paneLabel(pane paneInfo) string {
	label := pane.PaneID
	if pane.TerminalTitle != "" {
		label += " · " + pane.TerminalTitle
	} else if pane.Cwd != "" {
		label += " · " + shortenPath(pane.Cwd)
	}
	return truncateButtonLabel(label, 52)
}

func startPaneCard(page int) (string, *models.InlineKeyboardMarkup, error) {
	panes, err := listPanes()
	if err != nil {
		return "", nil, err
	}
	const pageSize = 8
	totalPages := (len(panes) + pageSize - 1) / pageSize
	if totalPages == 0 {
		totalPages = 1
	}
	if page < 0 {
		page = 0
	}
	if page >= totalPages {
		page = totalPages - 1
	}
	start, end := page*pageSize, (page+1)*pageSize
	if end > len(panes) {
		end = len(panes)
	}
	var rows [][]models.InlineKeyboardButton
	for _, pane := range panes[start:end] {
		token, ok := registerStartPane(pane)
		if !ok {
			continue
		}
		rows = append(rows, []models.InlineKeyboardButton{{Text: paneLabel(pane), CallbackData: startAgentPrefix + "pane|" + token}})
	}
	if totalPages > 1 {
		prev, next := page-1, page+1
		if prev < 0 {
			prev = 0
		}
		if next >= totalPages {
			next = totalPages - 1
		}
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: "Previous", CallbackData: fmt.Sprintf("%spage|%d", startAgentPrefix, prev)},
			{Text: fmt.Sprintf("%d/%d", page+1, totalPages), CallbackData: "al|noop"},
			{Text: "Next", CallbackData: fmt.Sprintf("%spage|%d", startAgentPrefix, next)},
		})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: "Cancel", CallbackData: "al|back"}})
	message := "<b>Start an agent</b>\n\nSelect the pane where the agent process is running."
	if len(panes) == 0 {
		message += "\n\nNo panes are available."
	}
	return message, &models.InlineKeyboardMarkup{InlineKeyboard: rows}, nil
}

func handleStartAgentWizard(ctx context.Context, b *bot.Bot, chatID int64, msgID int, page int) {
	stopTyping := startTyping(ctx, b, chatID)
	message, keyboard, err := startPaneCard(page)
	stopTyping()
	if err != nil {
		message = "Could not load panes: " + escapeHTML(err.Error())
		keyboard = &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "Back to agents", CallbackData: "al|back"}}}}
	}
	if msgID > 0 {
		editFormattedWithKeyboard(ctx, b, chatID, msgID, message, keyboard)
		return
	}
	sendFormattedWithKeyboard(ctx, b, chatID, message, keyboard)
}

func startAgentKindCard(pane paneInfo, token string) (string, *models.InlineKeyboardMarkup) {
	message := fmt.Sprintf("<b>Start an agent</b>\n\nPane: <code>%s</code>\nDirectory: <code>%s</code>\n\nSelect the agent kind.", escapeHTML(pane.PaneID), escapeHTML(shortenPath(pane.Cwd)))
	keyboard := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{
			{Text: "OpenCode", CallbackData: startAgentPrefix + "kind|" + token + "|opencode"},
			{Text: "Claude Code", CallbackData: startAgentPrefix + "kind|" + token + "|claude"},
			{Text: "Codex", CallbackData: startAgentPrefix + "kind|" + token + "|codex"},
		},
		{{Text: "Back to panes", CallbackData: startAgentPrefix + "page|0"}},
	}}
	return message, keyboard
}

func requestAgentName(ctx context.Context, b *bot.Bot, chatID, ownerID int64, pane paneInfo, kind string) {
	message := fmt.Sprintf("Reply with a name for the new <b>%s</b> agent in pane <code>%s</code>.\n\nUse letters, numbers, dots, underscores, or hyphens. The request expires in 10 minutes.", escapeHTML(kind), escapeHTML(pane.PaneID))
	sent, err := b.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: chatID, Text: message, ParseMode: models.ParseModeHTML,
		ReplyMarkup: &models.ForceReply{ForceReply: true, InputFieldPlaceholder: "Agent name", Selective: true},
	})
	if err != nil {
		log.Printf("ERROR requesting agent name: %v", err)
		return
	}
	startWizardState.Lock()
	startWizardState.replies[pendingPromptKey{ChatID: chatID, MessageID: sent.ID}] = pendingStart{
		OwnerID: ownerID, Pane: pane, Kind: kind, ExpiresAt: time.Now().Add(10 * time.Minute),
	}
	startWizardState.Unlock()
}

func startAgentCallbackHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.CallbackQuery == nil || !strings.HasPrefix(update.CallbackQuery.Data, startAgentPrefix) {
		return
	}
	chatID, msgID, ok := callbackChatInfo(update)
	if !ok || !isAuthorized(update.CallbackQuery.From.ID, chatID) {
		return
	}
	b.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: update.CallbackQuery.ID})
	parts := strings.Split(strings.TrimPrefix(update.CallbackQuery.Data, startAgentPrefix), "|")
	if len(parts) < 2 {
		return
	}
	switch parts[0] {
	case "page":
		page, _ := strconv.Atoi(parts[1])
		handleStartAgentWizard(ctx, b, chatID, msgID, page)
	case "pane":
		pane, ok := getStartPane(parts[1], false)
		if !ok {
			expiredActionCard(ctx, b, chatID, msgID)
			return
		}
		terminalID, err := herdrPaneTerminalID(pane.PaneID)
		if err != nil || terminalID != pane.TerminalID {
			expiredActionCard(ctx, b, chatID, msgID)
			return
		}
		message, keyboard := startAgentKindCard(pane, parts[1])
		editFormattedWithKeyboard(ctx, b, chatID, msgID, message, keyboard)
	case "kind":
		if len(parts) != 3 || (parts[2] != "opencode" && parts[2] != "claude" && parts[2] != "codex") {
			return
		}
		pane, ok := getStartPane(parts[1], true)
		if !ok {
			expiredActionCard(ctx, b, chatID, msgID)
			return
		}
		requestAgentName(ctx, b, chatID, update.CallbackQuery.From.ID, pane, parts[2])
		editMessageText(ctx, b, chatID, msgID, "Waiting for the agent name…")
	}
}

func pendingStartReplyMatch(update *models.Update) bool {
	if update.Message == nil || update.Message.From == nil || update.Message.ReplyToMessage == nil {
		return false
	}
	key := pendingPromptKey{ChatID: update.Message.Chat.ID, MessageID: update.Message.ReplyToMessage.ID}
	startWizardState.Lock()
	defer startWizardState.Unlock()
	entry, ok := startWizardState.replies[key]
	return ok && time.Now().Before(entry.ExpiresAt) && entry.OwnerID == update.Message.From.ID
}

func claimPendingStart(update *models.Update) (pendingStart, bool) {
	key := pendingPromptKey{ChatID: update.Message.Chat.ID, MessageID: update.Message.ReplyToMessage.ID}
	startWizardState.Lock()
	defer startWizardState.Unlock()
	entry, ok := startWizardState.replies[key]
	if !ok || time.Now().After(entry.ExpiresAt) || entry.OwnerID != update.Message.From.ID {
		delete(startWizardState.replies, key)
		return pendingStart{}, false
	}
	delete(startWizardState.replies, key)
	return entry, true
}

func startAgentReplyHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !pendingStartReplyMatch(update) || !isAuthorized(update.Message.From.ID, update.Message.Chat.ID) {
		return
	}
	pending, ok := claimPendingStart(update)
	if !ok {
		return
	}
	name := strings.TrimSpace(update.Message.Text)
	if !agentNamePattern.MatchString(name) {
		sendText(ctx, b, update.Message.Chat.ID, "Invalid agent name. Use 1-64 letters, numbers, dots, underscores, or hyphens, starting with a letter or number. Start the wizard again.")
		return
	}
	stopTyping := startTyping(ctx, b, update.Message.Chat.ID)
	terminalID, err := herdrPaneTerminalID(pending.Pane.PaneID)
	if err == nil && terminalID != pending.Pane.TerminalID {
		err = fmt.Errorf("pane terminal changed")
	}
	var output string
	if err == nil {
		output, err = paneOperations.run("terminal:"+pending.Pane.TerminalID, func() (string, error) {
			latestTerminalID, checkErr := herdrPaneTerminalID(pending.Pane.PaneID)
			if checkErr != nil || latestTerminalID != pending.Pane.TerminalID {
				return "", fmt.Errorf("pane changed while starting agent")
			}
			return herdrAgentStart(name, pending.Kind, pending.Pane.PaneID)
		})
	}
	stopTyping()
	if err != nil {
		sendText(ctx, b, update.Message.Chat.ID, "Could not start agent: "+err.Error())
		return
	}
	agent := agentInfo{Name: name, Agent: pending.Kind, AgentStatus: "working", PaneID: pending.Pane.PaneID, TerminalID: pending.Pane.TerminalID, Cwd: pending.Pane.Cwd}
	recordAgentActivity("started", agent)
	message := fmt.Sprintf("Started <b>%s</b> in pane <code>%s</code>.", escapeHTML(name), escapeHTML(pending.Pane.PaneID))
	if output != "" {
		message += "\n\n<code>" + escapeHTMLLimited(output, 1000) + "</code>"
	}
	sendFormattedWithKeyboard(ctx, b, update.Message.Chat.ID, message, &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{Text: "Open agents", CallbackData: "al|back"}}}})
}
