package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func herdrBin() string {
	bin := os.Getenv("HERDR_BIN_PATH")
	if bin == "" {
		bin = "herdr"
	}
	return bin
}

func runCommand(ctx context.Context, args ...string) (string, error) {
	bin := herdrBin()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("herdr command failed: %w (stderr: %s)", err, string(exitErr.Stderr))
		}
		return "", fmt.Errorf("herdr command failed: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

var herdrAgentList = func() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return runCommand(ctx, "agent", "list")
}

func herdrAgentGet(target string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return runCommand(ctx, "agent", "get", target)
}

func herdrAgentExplain(target string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return runCommand(ctx, "agent", "explain", target, "--json")
}

// herdrAgentRead reads recent agent output. The target may be either an agent
// name (as used by the /read command) or a pane ID (as used by the inline
// buttons and the watcher); herdr resolves both.
func herdrAgentRead(target string, lines int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lineArg := fmt.Sprintf("%d", lines)
	return runCommand(ctx, "agent", "read", target, "--source", "recent-unwrapped", "--lines", lineArg)
}

func herdrAgentReadVisible(target string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return runCommand(ctx, "agent", "read", target, "--source", "visible")
}

func herdrAgentPrompt(target, text string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return runCommand(ctx, "agent", "prompt", target, text)
}

func herdrAgentSendKeys(target string, keys ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return runCommand(ctx, append([]string{"agent", "send-keys", target}, keys...)...)
}

func herdrAgentStart(name, kind, paneID string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return runCommand(ctx, agentStartCommandArgs(name, kind, paneID, args...)...)
}

func agentStartCommandArgs(name, kind, paneID string, args ...string) []string {
	cmdArgs := []string{"agent", "start", name, "--kind", kind, "--pane", paneID}
	if len(args) > 0 {
		cmdArgs = append(cmdArgs, "--")
		cmdArgs = append(cmdArgs, args...)
	}
	return cmdArgs
}

// normalizeAgentReadOutput supports current Herdr, which returns terminal text
// directly, and the JSON envelope returned by older 0.7.x releases.
func normalizeAgentReadOutput(raw string) string {
	var envelope agentReadEnvelope
	if json.Unmarshal([]byte(raw), &envelope) == nil && len(envelope.Result) > 0 {
		var result agentReadResult
		if json.Unmarshal(envelope.Result, &result) == nil && result.Read.Text != "" {
			return strings.TrimSpace(result.Read.Text)
		}
	}
	return strings.TrimSpace(raw)
}

func herdrPaneTerminalID(paneID string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := runCommand(ctx, "pane", "get", paneID)
	if err != nil {
		return "", err
	}
	var result struct {
		Result struct {
			Pane struct {
				TerminalID string `json:"terminal_id"`
			} `json:"pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return "", fmt.Errorf("parsing pane get JSON for %s: %w", paneID, err)
	}
	if result.Result.Pane.TerminalID == "" {
		return "", fmt.Errorf("no terminal_id found for pane %s", paneID)
	}
	return result.Result.Pane.TerminalID, nil
}

var herdrPaneList = func() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return runCommand(ctx, "pane", "list")
}

func herdrPaneSendText(paneID, text string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return runCommand(ctx, "pane", "send-text", paneID, text)
}

func herdrPaneClose(paneID string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return runCommand(ctx, "pane", "close", paneID)
}
