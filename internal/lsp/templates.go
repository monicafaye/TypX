package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
)

// ErrTemplateCommandUnsupported is returned when the connected tinymist server
// is unavailable or does not advertise tinymist template RPC commands.
var ErrTemplateCommandUnsupported = errors.New("tinymist template command unavailable")

// InitTemplate delegates project template initialization (`@preview/...` or
// `@local/...`) to tinymist's `tinymist.doInitTemplate` command and returns the
// relative entrypoint path (e.g., "main.typ").
func InitTemplate(ctx context.Context, spec, targetDir string) (string, error) {
	return defaultManager.InitTemplate(ctx, spec, targetDir)
}

// GetTemplateEntry retrieves the raw entrypoint Typst source for spec via
// tinymist's `tinymist.doGetTemplateEntry` command.
func GetTemplateEntry(ctx context.Context, spec string) (string, error) {
	return defaultManager.GetTemplateEntry(ctx, spec)
}

// InitTemplate invokes `tinymist.doInitTemplate` on the active LSP client.
func (m *Manager) InitTemplate(ctx context.Context, spec, targetDir string) (string, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", errors.New("template spec is empty")
	}
	targetDir = strings.TrimSpace(targetDir)
	if targetDir == "" {
		return "", errors.New("target directory is empty")
	}
	absTarget, err := filepath.Abs(targetDir)
	if err != nil {
		return "", err
	}

	c, err := m.templateCommandClient("tinymist.doInitTemplate")
	if err != nil {
		return "", err
	}

	var raw json.RawMessage
	if err := c.Call(ctx, "workspace/executeCommand", ExecuteCommandParams{
		Command:   "tinymist.doInitTemplate",
		Arguments: []any{spec, absTarget},
	}, &raw); err != nil {
		return "", err
	}

	var obj struct {
		EntryPath string `json:"entryPath"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.EntryPath != "" {
		return obj.EntryPath, nil
	}
	var bare string
	if err := json.Unmarshal(raw, &bare); err == nil && bare != "" {
		return bare, nil
	}
	return "main.typ", nil
}

// GetTemplateEntry invokes `tinymist.doGetTemplateEntry` on the active LSP client.
func (m *Manager) GetTemplateEntry(ctx context.Context, spec string) (string, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return "", errors.New("template spec is empty")
	}

	c, err := m.templateCommandClient("tinymist.doGetTemplateEntry")
	if err != nil {
		return "", err
	}

	var raw json.RawMessage
	if err := c.Call(ctx, "workspace/executeCommand", ExecuteCommandParams{
		Command:   "tinymist.doGetTemplateEntry",
		Arguments: []any{spec},
	}, &raw); err != nil {
		return "", err
	}

	var content string
	if err := json.Unmarshal(raw, &content); err == nil {
		return content, nil
	}
	var obj struct {
		Content string `json:"content"`
		Entry   string `json:"entry"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		if obj.Content != "" {
			return obj.Content, nil
		}
		if obj.Entry != "" {
			return obj.Entry, nil
		}
	}
	return "", errors.New("unexpected response format from tinymist.doGetTemplateEntry")
}

func (m *Manager) templateCommandClient(command string) (*Client, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shuttingDown || m.State() != StateReady || m.client == nil || !m.serverCommands[command] {
		return nil, ErrTemplateCommandUnsupported
	}
	return m.client, nil
}
