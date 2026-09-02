package ui

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/azohra/config/internal/config"
)

const (
	maxOperationProgressBytes   = 128 << 10
	maxOperationProgressLines   = 256
	maxOperationDiagnosticBytes = 128 << 10
	maxOperationDiagnosticLines = 200
)

type outputSpan struct {
	kind config.OperationEventKind
	text []byte
}

type terminalOutput struct {
	spans        []outputSpan
	escape       byte
	pendingCR    bool
	storedSize   int
	omittedLines int
	maxBytes     int
	maxLines     int
}

func outputFromString(text string) terminalOutput {
	output := newTerminalOutput(maxOperationProgressBytes, maxOperationProgressLines)
	output.appendText(config.OperationOutput, text)
	output.bound()
	return output
}

func newTerminalOutput(maxBytes, maxLines int) terminalOutput {
	return terminalOutput{maxBytes: maxBytes, maxLines: maxLines}
}

func newOperationLog() operationLog {
	return operationLog{
		progress:    newTerminalOutput(maxOperationProgressBytes, maxOperationProgressLines),
		diagnostics: newTerminalOutput(maxOperationDiagnosticBytes, maxOperationDiagnosticLines),
	}
}

// operationLog separates Config's durable, typed account of an operation from
// the provider transcript that happens to accompany it. Provider output still
// remains available for diagnosis, but it cannot become the primary UI.
type operationLog struct {
	progress    terminalOutput
	diagnostics terminalOutput
	activity    string
}

func (o *operationLog) Append(event config.OperationEvent) {
	o.ensureLimits()
	if event.Kind == config.OperationOutput {
		o.diagnostics.Append(event)
		if activity := lastNonblankLine(o.diagnostics.String()); activity != "" {
			o.activity = activity
		}
		return
	}
	o.activity = ""
	o.progress.Append(event)
}

func (o *operationLog) ensureLimits() {
	o.progress.ensureLimits(maxOperationProgressBytes, maxOperationProgressLines)
	o.diagnostics.ensureLimits(maxOperationDiagnosticBytes, maxOperationDiagnosticLines)
}

func (o operationLog) hasDiagnostics() bool {
	return strings.TrimSpace(o.diagnostics.String()) != ""
}

func lastNonblankLine(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		if line := strings.TrimSpace(lines[index]); line != "" {
			return line
		}
	}
	return ""
}

func (o *terminalOutput) Append(event config.OperationEvent) {
	o.ensureLimits(maxOperationProgressBytes, maxOperationProgressLines)
	switch event.Kind {
	case config.OperationSection:
		o.appendText(config.OperationOutput, "\n"+event.Text+"\n")
	case config.OperationOK, config.OperationInfo, config.OperationWarn, config.OperationError:
		glyph := config.GlyphInfo
		switch event.Kind {
		case config.OperationOK:
			glyph = config.GlyphOK
		case config.OperationWarn:
			glyph = config.GlyphWarn
		case config.OperationError:
			glyph = config.GlyphError
		}
		o.appendText(config.OperationOutput, "  ")
		o.appendText(event.Kind, glyph)
		o.appendText(config.OperationOutput, " "+event.Text+"\n")
	case config.OperationVersion:
		return
	default:
		o.appendText(config.OperationOutput, event.Text)
	}
	o.bound()
}

func (o *terminalOutput) ensureLimits(maxBytes, maxLines int) {
	if o.maxBytes == 0 {
		o.maxBytes = maxBytes
	}
	if o.maxLines == 0 {
		o.maxLines = maxLines
	}
}

func (o *terminalOutput) appendText(kind config.OperationEventKind, text string) {
	for index := 0; index < len(text); index++ {
		value := text[index]
		if o.pendingCR {
			o.pendingCR = false
			if value == '\n' {
				o.appendByte(config.OperationOutput, '\n')
				continue
			}
			o.clearLine()
		}
		switch o.escape {
		case 1:
			switch value {
			case '[':
				o.escape = 2
			case ']':
				o.escape = 3
			default:
				o.escape = 0
			}
			continue
		case 2:
			if value >= 0x40 && value <= 0x7e {
				o.escape = 0
			}
			continue
		case 3:
			switch value {
			case '\a':
				o.escape = 0
			case 0x1b:
				o.escape = 4
			}
			continue
		case 4:
			if value == '\\' {
				o.escape = 0
			} else if value != 0x1b {
				o.escape = 3
			}
			continue
		}
		switch value {
		case 0x1b:
			o.escape = 1
		case '\r':
			o.pendingCR = true
		case '\b':
			o.backspace()
		default:
			if value == '\n' || value == '\t' || (value >= ' ' && value != 0x7f) {
				o.appendByte(kind, value)
			}
		}
	}
}

func (o *terminalOutput) appendByte(kind config.OperationEventKind, value byte) {
	last := len(o.spans) - 1
	if last >= 0 && o.spans[last].kind == kind {
		o.spans[last].text = append(o.spans[last].text, value)
	} else {
		o.spans = append(o.spans, outputSpan{kind: kind, text: []byte{value}})
	}
	o.storedSize++
}

func (o *terminalOutput) clearLine() {
	for o.storedSize > 0 {
		last := len(o.spans) - 1
		if len(o.spans[last].text) > 0 && o.spans[last].text[len(o.spans[last].text)-1] == '\n' {
			return
		}
		o.removeLastRune()
	}
}

func (o *terminalOutput) backspace() {
	if o.storedSize == 0 {
		return
	}
	last := len(o.spans) - 1
	if len(o.spans[last].text) > 0 && o.spans[last].text[len(o.spans[last].text)-1] == '\n' {
		return
	}
	o.removeLastRune()
}

func (o *terminalOutput) removeLastRune() {
	last := len(o.spans) - 1
	if last < 0 {
		return
	}
	_, size := utf8.DecodeLastRune(o.spans[last].text)
	if size == 0 {
		return
	}
	o.spans[last].text = o.spans[last].text[:len(o.spans[last].text)-size]
	o.storedSize -= size
	if len(o.spans[last].text) == 0 {
		o.spans = o.spans[:last]
	}
}

func (o *terminalOutput) bound() {
	text := o.String()
	start := byteBoundStart(text, o.maxBytes)
	start = max(start, lineBoundStart(text, o.maxLines))
	if start == 0 {
		return
	}
	o.omittedLines += strings.Count(text[:start], "\n")
	if text[start-1] != '\n' {
		o.omittedLines++
	}
	o.dropPrefix(start)
}

func byteBoundStart(text string, limit int) int {
	if limit <= 0 || len(text) <= limit {
		return 0
	}
	start := len(text) - limit
	if newline := strings.IndexByte(text[start:], '\n'); newline >= 0 {
		return start + newline + 1
	}
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return start
}

func lineBoundStart(text string, limit int) int {
	if limit <= 0 || text == "" {
		return 0
	}
	lines := strings.Count(text, "\n")
	if text[len(text)-1] != '\n' {
		lines++
	}
	drop := lines - limit
	if drop <= 0 {
		return 0
	}
	start := 0
	for range drop {
		newline := strings.IndexByte(text[start:], '\n')
		if newline < 0 {
			return len(text)
		}
		start += newline + 1
	}
	return start
}

func (o *terminalOutput) dropPrefix(size int) {
	for size > 0 && len(o.spans) > 0 {
		if size >= len(o.spans[0].text) {
			size -= len(o.spans[0].text)
			o.storedSize -= len(o.spans[0].text)
			o.spans = o.spans[1:]
			continue
		}
		o.spans[0].text = o.spans[0].text[size:]
		o.storedSize -= size
		size = 0
	}
}

func (o terminalOutput) String() string {
	var text strings.Builder
	text.Grow(o.storedSize)
	for _, span := range o.spans {
		text.Write(span.text)
	}
	return text.String()
}

func (o terminalOutput) Styled() string {
	var text strings.Builder
	for _, span := range o.spans {
		value := string(span.text)
		switch span.kind {
		case config.OperationOK:
			text.WriteString(good.Render(value))
		case config.OperationInfo:
			text.WriteString(accent.Render(value))
		case config.OperationWarn:
			text.WriteString(caution.Render(value))
		case config.OperationError:
			text.WriteString(bad.Render(value))
		default:
			text.WriteString(value)
		}
	}
	return text.String()
}

func (o terminalOutput) Lines(width int, omissionNoun string) []string {
	output := strings.Trim(ansi.Hardwrap(o.Styled(), width, true), "\n")
	var lines []string
	if o.omittedLines > 0 {
		lines = append(lines, omissionLine(o.omittedLines, omissionNoun))
	}
	if output != "" {
		lines = append(lines, strings.Split(output, "\n")...)
	}
	return lines
}

// TailLines wraps only enough logical lines to fill a live viewport. The
// completed result can afford to build its bounded scroll model, but the
// spinner redraws this path continuously while a provider is running.
func (o terminalOutput) TailLines(width, available int, omissionNoun string) []string {
	if available <= 0 {
		return nil
	}
	output := strings.Trim(o.Styled(), "\n")
	var lines []string
	if output != "" {
		logical := strings.Split(output, "\n")
		for index := len(logical) - 1; index >= 0 && len(lines) < available; index-- {
			wrapped := strings.Split(ansi.Hardwrap(logical[index], width, true), "\n")
			remaining := available - len(lines)
			if len(wrapped) > remaining {
				wrapped = wrapped[len(wrapped)-remaining:]
			}
			lines = append(wrapped, lines...)
		}
	}
	if o.omittedLines > 0 {
		if len(lines) == available {
			lines = lines[1:]
		}
		lines = append([]string{omissionLine(o.omittedLines, omissionNoun)}, lines...)
	}
	return lines
}

func omissionLine(count int, noun string) string {
	return muted.Render("… " + config.FormatCount(count, "earlier "+noun+" omitted", "earlier "+noun+"s omitted"))
}
