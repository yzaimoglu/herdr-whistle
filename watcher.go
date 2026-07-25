package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func agentTrackingIdentity(agent agentInfo) string {
	if agent.AgentSession.Value != "" {
		return "session:" + agent.AgentSession.Value
	}
	return fmt.Sprintf("state:%s:%d", agent.TerminalID, agent.StateChangeSeq)
}

type agentLifecycleEvent struct {
	Kind  string
	Agent agentInfo
}

// agentWatcher polls herdr agent list and dispatches lifecycle transitions.
func agentWatcher(ctx context.Context, b *bot.Bot, chatID int64) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// Track agent session keys to detect stale entries.
	// A session key uniquely identifies a running agent instance.
	type sessionKey struct {
		PaneID   string
		Identity string
		// agent_session.value is a UUID that changes when an agent restarts.
		// We track it so if an agent pane is closed and a new one starts with
		// the same pane_id, we treat it as a fresh session.
	}

	prevStatus := map[sessionKey]string{}
	initialized := false

	// Notifications are dispatched to a dedicated goroutine so the slow parts
	// of notifyBlocked (a herdr agent read and a Telegram send, each with a
	// 30s timeout) cannot stall the 5s polling cadence. The buffer absorbs
	// bursts; if it ever fills we drop+log rather than block the poll loop.
	notifyCh := make(chan agentLifecycleEvent, 64)
	var notifyWG sync.WaitGroup
	notifyWG.Add(1)
	go func() {
		defer notifyWG.Done()
		for event := range notifyCh {
			notifyAgentLifecycle(ctx, b, chatID, event)
		}
	}()

	refresh := func() {
		out, err := herdrAgentList()
		if err != nil {
			log.Printf("WARN agentWatcher: herdr agent list failed: %v", err)
			return
		}

		var env agentListEnvelope
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			log.Printf("WARN agentWatcher: parsing envelope: %v", err)
			return
		}
		var lr agentListResult
		if err := json.Unmarshal(env.Result, &lr); err != nil {
			log.Printf("WARN agentWatcher: parsing result: %v", err)
			return
		}

		// Collect agents that transitioned into "blocked" this poll. The poll
		// loop is the only goroutine touching prevStatus, so no lock is needed.
		seen := map[sessionKey]bool{}
		var toNotify []agentLifecycleEvent
		for _, a := range lr.Agents {
			if a.PaneID == "" {
				continue
			}
			sk := sessionKey{PaneID: a.PaneID, Identity: agentTrackingIdentity(a)}
			seen[sk] = true

			oldStatus, exists := prevStatus[sk]
			currStatus := strings.ToLower(a.AgentStatus)

			if currStatus == "blocked" && (!exists || oldStatus != "blocked") {
				toNotify = append(toNotify, agentLifecycleEvent{Kind: "blocked", Agent: a})
			}
			if exists && oldStatus == "blocked" && currStatus != "blocked" {
				toNotify = append(toNotify, agentLifecycleEvent{Kind: "resolved", Agent: a})
			} else if exists && (currStatus == "done" || currStatus == "idle") && oldStatus != "done" && oldStatus != "idle" {
				toNotify = append(toNotify, agentLifecycleEvent{Kind: "completed", Agent: a})
			}
			if initialized && !exists {
				toNotify = append(toNotify, agentLifecycleEvent{Kind: "started", Agent: a})
			}

			prevStatus[sk] = currStatus
		}

		// Remove stale entries for panes that no longer exist.
		for sk := range prevStatus {
			if !seen[sk] {
				delete(prevStatus, sk)
			}
		}

		initialized = true
		for _, event := range toNotify {
			select {
			case notifyCh <- event:
			default:
				log.Printf("WARN agentWatcher: notification queue full, dropping %s event for %s", event.Kind, event.Agent.Agent)
			}
		}
	}

	// Do an immediate refresh on start to seed state.
	refresh()

	for {
		select {
		case <-ctx.Done():
			// Close the channel so the notifier drains and exits, then wait
			// for any in-flight notification to finish (deterministic shutdown).
			close(notifyCh)
			notifyWG.Wait()
			return
		case <-ticker.C:
			refresh()
		}
	}
}

func notifyAgentLifecycle(ctx context.Context, b *bot.Bot, chatID int64, event agentLifecycleEvent) {
	switch event.Kind {
	case "blocked":
		recordAgentActivity("blocked", event.Agent)
		if currentPreferences().NotifyBlocked {
			notifyBlocked(ctx, b, chatID, event.Agent)
		}
	case "resolved":
		recordAgentActivity("resolved", event.Agent)
		resolveBlockedMessage(ctx, b, event.Agent)
	case "completed":
		recordAgentActivity("completed", event.Agent)
		if chatID > 0 && currentPreferences().NotifyCompleted {
			message := fmt.Sprintf("✅ <b>%s</b> completed and is now %s.", escapeHTML(agentUILabel(event.Agent)), escapeHTML(strings.ToLower(event.Agent.AgentStatus)))
			sendFormattedWithKeyboard(ctx, b, chatID, message, agentOpenKeyboard(event.Agent))
		}
	case "started":
		recordAgentActivity("started", event.Agent)
	}
}

func resolveBlockedMessage(ctx context.Context, b *bot.Bot, agent agentInfo) {
	ref, ok := takeBlockedMessage(agent.TerminalID)
	if !ok {
		return
	}
	icon, status := agentStatusPresentation(agent.AgentStatus)
	message := fmt.Sprintf("%s <b>%s</b> is no longer waiting for input.\nStatus: %s", icon, escapeHTML(agentUILabel(agent)), escapeHTML(status))
	editFormattedWithKeyboard(ctx, b, ref.ChatID, ref.MessageID, message, agentOpenKeyboard(agent))
}

func readPaneText(paneID string) string {
	raw, err := herdrAgentReadVisible(paneID)
	if err != nil {
		return ""
	}
	return normalizeAgentReadOutput(raw)
}

func revalidateBlockedAgent(expected agentInfo) (agentInfo, bool) {
	current, err := currentAgentByPane(expected.PaneID)
	if err != nil || strings.ToLower(current.AgentStatus) != "blocked" {
		return agentInfo{}, false
	}
	if validateAgentSnapshot(expected, current) != nil {
		return agentInfo{}, false
	}
	return current, true
}

func blockedActionKeyboard(agent agentInfo, keyboard *models.InlineKeyboardMarkup) *models.InlineKeyboardMarkup {
	token, ok := agentUITokens.register(agent)
	if !ok {
		return keyboard
	}
	if keyboard == nil {
		keyboard = &models.InlineKeyboardMarkup{}
	}
	keyboard.InlineKeyboard = append(keyboard.InlineKeyboard,
		[]models.InlineKeyboardButton{
			{Text: "Send response", CallbackData: "al|prompt|" + token},
			{Text: "Read output", CallbackData: "al|output|" + token},
		},
		[]models.InlineKeyboardButton{
			{Text: "Open agent", CallbackData: "al|open|" + token},
		},
	)
	return keyboard
}

func blockedTextChoiceKeyboard(agent agentInfo, choices *parsedChoices) *models.InlineKeyboardMarkup {
	if choices == nil || choices.MultiSelect || len(choices.Choices) == 0 || len(choices.Choices) > 10 {
		return nil
	}
	keyboard := &models.InlineKeyboardMarkup{}
	group, ok := randomUIToken()
	if !ok {
		return nil
	}
	fingerprint := choiceMenuFingerprint(choices)
	for i, choice := range choices.Choices {
		lower := strings.ToLower(choice.CleanText)
		if strings.Contains(lower, "type your own") || strings.Contains(lower, "custom answer") {
			continue
		}
		if choice.SubmitText == "" {
			continue
		}
		token, ok := pendingBlockedResponses.register(agent, choice.SubmitText, fingerprint, group)
		if !ok {
			return nil
		}
		keyboard.InlineKeyboard = append(keyboard.InlineKeyboard, []models.InlineKeyboardButton{{
			Text:         truncateButtonLabel(strconv.Itoa(i+1)+" · "+choice.CleanText, 52),
			CallbackData: "br|" + token,
		}})
	}
	if len(keyboard.InlineKeyboard) == 0 {
		return nil
	}
	return keyboard
}

func sendBlockedNotification(ctx context.Context, b *bot.Bot, chatID int64, agent agentInfo, message string, keyboard *models.InlineKeyboardMarkup) {
	sent := sendFormattedWithKeyboard(ctx, b, chatID, message, keyboard)
	if sent != nil {
		rememberBlockedMessage(agent, chatID, sent.ID)
	}
}

var notifyBlocked = func(ctx context.Context, b *bot.Bot, chatID int64, a agentInfo) {
	current, ok := revalidateBlockedAgent(a)
	if !ok {
		return
	}
	a = current
	shortCwd := shortenPath(a.Cwd)
	paneID := a.PaneID

	var sb strings.Builder
	sb.WriteString("⏸ <b>")
	sb.WriteString(escapeHTMLLimited(agentUILabel(a), 180))
	sb.WriteString("</b> is blocked and waiting for input.\n")
	if shortCwd != "" {
		sb.WriteString("   ")
		sb.WriteString(escapeHTMLLimited(shortCwd, 240))
		sb.WriteString("\n")
	}
	sb.WriteString("Pane: ")
	sb.WriteString(escapeHTMLLimited(paneID, 100))
	if a.Focused {
		sb.WriteString(" 👁")
	}

	// @clack/prompts redraws the selection in raw mode, so the choices may be
	// at the upper boundary of the scrollback buffer.
	text := readPaneText(paneID)

	pc := parseAgentChoicesFor(a.Agent, text)
	current, ok = revalidateBlockedAgent(a)
	if !ok {
		return
	}
	a = current
	if pc != nil {
		sb.WriteString("\n\n<b>")
		sb.WriteString(escapeHTMLLimited(pc.Prompt, 400))
		sb.WriteString("</b>")

		sb.WriteString("\n")
		displayedChoices := pc.Choices
		if len(displayedChoices) > 10 {
			displayedChoices = displayedChoices[:10]
		}
		for i, c := range displayedChoices {
			sb.WriteString("\n" + strconv.Itoa(i+1) + ". " + escapeHTMLLimited(c.CleanText, 180))
		}
		if len(displayedChoices) < len(pc.Choices) {
			sb.WriteString("\n...additional choices omitted")
		}

		msg := sb.String()
		var kb *models.InlineKeyboardMarkup
		if !pc.MultiSelect && (pc.ActiveIndex >= 0 || hasDirectChoiceKeys(pc)) && len(pc.Choices) <= 10 && utf8.RuneCountInString(msg) <= telegramMessageLimit {
			if nonce, ok := pendingChoices.register(a, pc); ok {
				kb = buildChoiceKeyboard(pc, nonce)
			}
		}
		if kb == nil {
			kb = blockedTextChoiceKeyboard(a, pc)
		}
		automaticChoices := kb != nil
		kb = blockedActionKeyboard(a, kb)
		if !automaticChoices {
			msg += "\n\nUse <b>Send response</b> for this prompt; direct selection is unavailable because the current cursor cannot be verified."
		}
		if utf8.RuneCountInString(msg) > telegramMessageLimit {
			msg = fmt.Sprintf("⏸ <b>%s</b> is blocked and waiting for input.\nPane: <code>%s</code>\n\nThe prompt was too long to display safely; use <b>Read output</b> or <b>Send response</b>.",
				escapeHTMLLimited(agentUILabel(a), 180), escapeHTMLLimited(a.PaneID, 100))
		}
		sendBlockedNotification(ctx, b, chatID, a, msg, kb)
		return
	}

	if text != "" {
		lines := strings.Split(text, "\n")
		tail := 3
		if len(lines) < tail {
			tail = len(lines)
		}
		contextText := strings.TrimSpace(strings.Join(lines[len(lines)-tail:], "\n"))
		if contextText != "" {
			sb.WriteString("\n\n<code>")
			sb.WriteString(escapeHTMLLimited(contextText, 900))
			sb.WriteString("</code>")
		}
	}

	sendBlockedNotification(ctx, b, chatID, a, sb.String(), blockedActionKeyboard(a, nil))
}
