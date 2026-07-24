package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

const activityPageSize = 8

func activityCard(page int) (string, *models.InlineKeyboardMarkup) {
	events, totalPages := activityPage(page, activityPageSize)

	var message strings.Builder
	message.WriteString("<b>Recent activity</b>\n")
	if len(events) == 0 {
		message.WriteString("\nNo activity recorded yet.")
	} else {
		for _, event := range events {
			message.WriteString(fmt.Sprintf("\n<code>%s</code>  <b>%s</b>\n%s", event.At.Local().Format("15:04"), escapeHTML(event.Agent), escapeHTML(event.Summary)))
		}
	}

	var rows [][]models.InlineKeyboardButton
	if totalPages > 1 {
		prev, next := page-1, page+1
		if prev < 0 {
			prev = 0
		}
		if next >= totalPages {
			next = totalPages - 1
		}
		rows = append(rows, []models.InlineKeyboardButton{
			{Text: "Previous", CallbackData: fmt.Sprintf("al|activity|%d", prev)},
			{Text: fmt.Sprintf("%d/%d", page+1, totalPages), CallbackData: "al|noop"},
			{Text: "Next", CallbackData: fmt.Sprintf("al|activity|%d", next)},
		})
	}
	rows = append(rows, []models.InlineKeyboardButton{{Text: "Back to agents", CallbackData: "al|back"}})
	return message.String(), &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func activityHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !ownerAuth(ctx, b, update) {
		return
	}
	message, keyboard := activityCard(0)
	sendFormattedWithKeyboard(ctx, b, update.Message.Chat.ID, message, keyboard)
}

func preferencesCard() (string, *models.InlineKeyboardMarkup) {
	prefs := currentPreferences()
	state := func(enabled bool) string {
		if enabled {
			return "On"
		}
		return "Off"
	}
	message := fmt.Sprintf("<b>Notification settings</b>\n\nNeeds input: <b>%s</b>\nAgent completed: <b>%s</b>\nAgent started: <b>%s</b>\n\nInterface: <b>%s</b>",
		state(prefs.NotifyBlocked), state(prefs.NotifyCompleted), state(prefs.NotifyStarted), escapeHTML(prefs.UIMode))
	keyboard := &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{
		{{Text: "Toggle needs input", CallbackData: "al|toggle_pref|blocked"}},
		{{Text: "Toggle completed", CallbackData: "al|toggle_pref|completed"}},
		{{Text: "Toggle started", CallbackData: "al|toggle_pref|started"}},
		{{Text: "Back to agents", CallbackData: "al|back"}},
	}}
	return message, keyboard
}

func notificationsHandler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if !ownerAuth(ctx, b, update) {
		return
	}
	message, keyboard := preferencesCard()
	sendFormattedWithKeyboard(ctx, b, update.Message.Chat.ID, message, keyboard)
}

func handleListView(ctx context.Context, b *bot.Bot, chatID int64, msgID int, payload string) {
	parts := strings.Split(payload, "|")
	if len(parts) != 2 {
		return
	}
	page, err := strconv.Atoi(parts[1])
	if err != nil {
		return
	}
	view := defaultListView()
	view.Filter = parts[0]
	view.Page = page
	message, keyboard, err := buildAgentListView(view)
	if err != nil {
		editMessageText(ctx, b, chatID, msgID, "Could not load agents: "+escapeHTML(err.Error()))
		return
	}
	editFormattedWithKeyboard(ctx, b, chatID, msgID, message, keyboard)
}

func handleActivityCard(ctx context.Context, b *bot.Bot, chatID int64, msgID int, payload string) {
	page, _ := strconv.Atoi(payload)
	message, keyboard := activityCard(page)
	editFormattedWithKeyboard(ctx, b, chatID, msgID, message, keyboard)
}

func handlePreferencesCard(ctx context.Context, b *bot.Bot, chatID int64, msgID int) {
	message, keyboard := preferencesCard()
	editFormattedWithKeyboard(ctx, b, chatID, msgID, message, keyboard)
}

func handlePreferenceToggle(ctx context.Context, b *bot.Bot, chatID int64, msgID int, name string) {
	if err := updatePreferences(func(prefs *userPreferences) {
		switch name {
		case "blocked":
			prefs.NotifyBlocked = !prefs.NotifyBlocked
		case "completed":
			prefs.NotifyCompleted = !prefs.NotifyCompleted
		case "started":
			prefs.NotifyStarted = !prefs.NotifyStarted
		}
	}); err != nil {
		editMessageText(ctx, b, chatID, msgID, "Could not save settings: "+escapeHTML(err.Error()))
		return
	}
	handlePreferencesCard(ctx, b, chatID, msgID)
}

func handleModeChange(ctx context.Context, b *bot.Bot, chatID int64, msgID int, mode string) {
	if mode != "compact" && mode != "dashboard" {
		return
	}
	if err := updatePreferences(func(prefs *userPreferences) { prefs.UIMode = mode }); err != nil {
		editMessageText(ctx, b, chatID, msgID, "Could not save interface mode: "+escapeHTML(err.Error()))
		return
	}
	handleRefresh(ctx, b, chatID, msgID)
}

func activityDetail(kind string) string {
	switch kind {
	case "blocked":
		return "Needs input"
	case "completed":
		return "Completed"
	case "started":
		return "Started"
	case "prompt":
		return "Prompt sent"
	case "response":
		return "Response sent"
	case "resolved":
		return "Input resolved"
	case "interrupted":
		return "Interrupted"
	case "closed":
		return "Pane closed"
	default:
		return kind
	}
}

func recordAgentActivity(kind string, agent agentInfo) {
	recordActivity(kind, agent, activityDetail(kind))
}
