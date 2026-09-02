package ui

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/azohra/config/internal/config"
)

type outputSpan struct {
	kind config.OperationEventKind
	text []byte
}

type terminalOutput struct {
	spans      []outputSpan
	escape     byte
	pendingCR  bool
	storedSize int
}

func outputFromString(text string) terminalOutput {
	var output terminalOutput
	output.appendText(config.OperationOutput, text)
	return output
}

func (o *terminalOutput) Append(event config.OperationEvent) {
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
	if o.storedSize <= maxOperationOutput {
		return
	}
	drop := o.storedSize - maxOperationOutput
	offset := 0
	start := drop
	foundNewline := false
	for _, span := range o.spans {
		if offset+len(span.text) <= drop {
			offset += len(span.text)
			continue
		}
		from := max(0, drop-offset)
		if newline := bytes.IndexByte(span.text[from:], '\n'); newline >= 0 {
			start = offset + from + newline + 1
			foundNewline = true
			break
		}
		offset += len(span.text)
	}
	if !foundNewline {
		text := o.String()
		for start < len(text) && !utf8.RuneStart(text[start]) {
			start++
		}
	}
	o.dropPrefix(start)
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

func (o terminalOutput) Tail(width, available int) string {
	return outputTail(ansi.Hardwrap(o.Styled(), width, true), available)
}
