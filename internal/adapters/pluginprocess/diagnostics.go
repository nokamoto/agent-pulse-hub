package pluginprocess

import (
	"log/slog"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const maxPluginDiagnosticBytes = 1_024

// diagnosticWriter drains arbitrary chunks without retaining unbounded lines.
type diagnosticWriter struct {
	mu        sync.Mutex
	logger    *slog.Logger
	plugin    string
	source    string
	line      []byte
	truncated bool
}

func (p *Process) newDiagnosticWriter(source string) *diagnosticWriter {
	return &diagnosticWriter{logger: p.logger, plugin: p.config.Name, source: source}
}

func (w *diagnosticWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, b := range data {
		if b == '\n' {
			w.flushLocked()
			continue
		}
		if len(w.line) < maxPluginDiagnosticBytes {
			w.line = append(w.line, b)
		} else {
			w.truncated = true
		}
	}
	return len(data), nil
}

func (w *diagnosticWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.flushLocked()
}

func (w *diagnosticWriter) flushLocked() {
	if len(w.line) == 0 && !w.truncated {
		return
	}
	message := strings.TrimSpace(safeText(string(w.line), maxPluginDiagnosticBytes))
	w.logger.Warn("plugin_diagnostic", "plugin", w.plugin, "source", w.source,
		"message", message, "truncated", w.truncated)
	w.line = w.line[:0]
	w.truncated = false
}

func safeText(value string, limit int) string {
	var output strings.Builder
	for _, character := range value {
		if unicode.IsControl(character) {
			character = ' '
		}
		if output.Len()+utf8.RuneLen(character) > limit {
			break
		}
		output.WriteRune(character)
	}
	return output.String()
}
