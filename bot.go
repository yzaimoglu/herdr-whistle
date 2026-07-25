package main

import (
	"context"
	"fmt"
	"html"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// startBot creates and starts the Telegram bot with all registered handlers.
func startBot(ctx context.Context, cfg *Config) error {
	cfgGlobal = cfg

	opts := []bot.Option{
		bot.WithDefaultHandler(defaultHandler),
	}

	var b *bot.Bot
	var err error

	// Retry bot.New() on transient failures (e.g. network blips).
	// The getMe call inside bot.New() can fail if Telegram is unreachable.
	backoff := time.Second
	maxRetries := 5
	for attempt := 1; attempt <= maxRetries; attempt++ {
		b, err = bot.New(cfg.Token, opts...)
		if err == nil {
			break
		}
		log.Printf("WARN creating bot (attempt %d/%d): %v", attempt, maxRetries, err)
		if attempt < maxRetries {
			log.Printf("retrying in %v...", backoff)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
		}
	}
	if err != nil {
		return fmt.Errorf("creating bot after %d retries: %w", maxRetries, err)
	}

	// Register command handlers.
	// MatchTypeCommand extracts the command name WITHOUT the leading "/",
	// so patterns must omit it (e.g. "start" not "/start").
	b.RegisterHandlerMatchFunc(pendingCustomChoiceReplyMatch, customChoiceReplyHandler)
	b.RegisterHandlerMatchFunc(pendingStartReplyMatch, startAgentReplyHandler)
	b.RegisterHandlerMatchFunc(pendingPromptReplyMatch, promptReplyHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "start", bot.MatchTypeCommand, startHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "agents", bot.MatchTypeCommand, agentsHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "status", bot.MatchTypeCommand, statusHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "read", bot.MatchTypeCommand, readHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "send", bot.MatchTypeCommand, sendHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "close", bot.MatchTypeCommand, closeHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "startagent", bot.MatchTypeCommand, startAgentHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "activity", bot.MatchTypeCommand, activityHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "notifications", bot.MatchTypeCommand, notificationsHandler)
	b.RegisterHandler(bot.HandlerTypeMessageText, "help", bot.MatchTypeCommand, helpHandler)

	// Register inline keyboard callback handlers.
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, cbPrefix, bot.MatchTypePrefix, agentsCallbackHandler)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, choiceCallbackPrefix, bot.MatchTypePrefix, choiceCallbackHandler)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, customChoiceCallbackPrefix, bot.MatchTypePrefix, customChoiceCallbackHandler)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, blockedResponsePrefix, bot.MatchTypePrefix, blockedResponseCallbackHandler)
	b.RegisterHandler(bot.HandlerTypeCallbackQueryData, startAgentPrefix, bot.MatchTypePrefix, startAgentCallbackHandler)

	// Close the inherited readiness descriptor before any watcher subprocess can
	// inherit it and keep the parent waiting after a worker failure.
	signalParentReady()
	go configureTelegramUI(ctx, b, cfg.ChatID)

	// Start the background agent watcher goroutine.
	// It polls herdr agent list every 5 seconds and notifies the owner
	// via Telegram when an agent becomes blocked.
	go agentWatcher(ctx, b, cfg.ChatID)

	log.Printf("Bot started, listening for commands...")
	b.Start(ctx)
	return nil
}

func configureTelegramUI(ctx context.Context, b *bot.Bot, chatID int64) {
	commands := []models.BotCommand{
		{Command: "start", Description: "Open the dashboard"},
		{Command: "agents", Description: "Browse agents"},
		{Command: "activity", Description: "Recent activity"},
		{Command: "startagent", Description: "Start an agent"},
		{Command: "notifications", Description: "Notification settings"},
		{Command: "status", Description: "Show agent status"},
		{Command: "read", Description: "Read agent output"},
		{Command: "send", Description: "Send an agent prompt"},
		{Command: "close", Description: "Close an agent pane"},
		{Command: "help", Description: "Show help"},
	}
	if _, err := b.SetMyCommands(ctx, &bot.SetMyCommandsParams{
		Commands: commands,
		Scope:    &models.BotCommandScopeChat{ChatID: chatID},
	}); err != nil {
		log.Printf("WARN configuring Telegram commands: %v", err)
	}
	if _, err := b.SetChatMenuButton(ctx, &bot.SetChatMenuButtonParams{
		ChatID: chatID,
		MenuButton: models.MenuButtonCommands{
			Type: models.MenuButtonTypeCommands,
		},
	}); err != nil {
		log.Printf("WARN configuring Telegram menu button: %v", err)
	}
}

func startTyping(ctx context.Context, b *bot.Bot, chatID int64) func() {
	typingCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		send := func() {
			_, _ = b.SendChatAction(typingCtx, &bot.SendChatActionParams{ChatID: chatID, Action: models.ChatActionTyping})
		}
		send()
		for {
			select {
			case <-typingCtx.Done():
				return
			case <-ticker.C:
				send()
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func signalParentReady() {
	value := os.Getenv("HERDR_WHISTLE_READY_FD")
	if value == "" {
		return
	}
	fd, err := strconv.Atoi(value)
	if err != nil || fd < 0 {
		return
	}
	file := os.NewFile(uintptr(fd), "herdr-whistle-ready")
	if file == nil {
		return
	}
	_, _ = file.Write([]byte{1})
	_ = file.Close()
}

// sendText sends a plain text message (no Markdown parsing).
func sendText(ctx context.Context, b *bot.Bot, chatID int64, text string) {
	for _, chunk := range splitTelegramText(text, telegramMessageLimit) {
		params := &bot.SendMessageParams{
			ChatID: chatID,
			Text:   chunk,
		}
		if _, err := b.SendMessage(ctx, params); err != nil {
			log.Printf("ERROR sending plain message: %v", err)
			return
		}
	}
}

// sendFormatted sends an HTML-formatted message.
// All text MUST be escapeHTML()-escaped before calling this.
func sendFormatted(ctx context.Context, b *bot.Bot, chatID int64, text string) {
	if utf8.RuneCountInString(text) > telegramMessageLimit {
		// Splitting arbitrary HTML can leave unbalanced tags. Fall back to plain
		// text so large output is still delivered safely in bounded chunks.
		plain := strings.NewReplacer(
			"<pre><code>", "", "</code></pre>", "",
			"<b>", "", "</b>", "",
			"<code>", "", "</code>", "",
		).Replace(text)
		sendText(ctx, b, chatID, html.UnescapeString(plain))
		return
	}
	params := &bot.SendMessageParams{
		ChatID:    chatID,
		Text:      text,
		ParseMode: models.ParseModeHTML,
	}
	if _, err := b.SendMessage(ctx, params); err != nil {
		log.Printf("ERROR sending formatted message: %v", err)
	}
}

func sendFormattedWithKeyboard(ctx context.Context, b *bot.Bot, chatID int64, text string, keyboard *models.InlineKeyboardMarkup) *models.Message {
	if utf8.RuneCountInString(text) > telegramMessageLimit {
		sendFormatted(ctx, b, chatID, text)
		return nil
	}
	params := &bot.SendMessageParams{
		ChatID:      chatID,
		Text:        text,
		ParseMode:   models.ParseModeHTML,
		ReplyMarkup: keyboard,
	}
	sent, err := b.SendMessage(ctx, params)
	if err != nil {
		log.Printf("ERROR sending formatted message with keyboard: %v", err)
		return nil
	}
	return sent
}

const telegramMessageLimit = 4000

func splitTelegramText(text string, limit int) []string {
	if limit <= 0 || text == "" {
		return []string{text}
	}
	runes := []rune(text)
	var chunks []string
	for len(runes) > limit {
		cut := limit
		for i := limit; i > limit/2; i-- {
			if runes[i-1] == '\n' {
				cut = i
				break
			}
		}
		chunks = append(chunks, string(runes[:cut]))
		runes = runes[cut:]
	}
	if len(runes) > 0 || len(chunks) == 0 {
		chunks = append(chunks, string(runes))
	}
	return chunks
}

func truncateText(text string, limit int) string {
	runes := []rune(text)
	if limit <= 0 || len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "\n...[truncated]"
}
