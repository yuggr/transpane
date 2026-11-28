// Package domain holds the values shared across transcript, monitor and TUI.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Provider identifies the source application.
type Provider string

const (
	ProviderCodex  Provider = "codex"
	ProviderClaude Provider = "claude"
)

func (p Provider) Label() string {
	switch p {
	case ProviderClaude:
		return "Claude Code"
	default:
		return "Codex"
	}
}

// ShortLabel is the compact picker label: Codex and CC.
func (p Provider) ShortLabel() string {
	switch p {
	case ProviderClaude:
		return "CC"
	default:
		return "Codex"
	}
}

// MessageKind is the display classification of one transcript message.
type MessageKind string

const (
	MessageKindUser       MessageKind = "user"
	MessageKindCommentary MessageKind = "commentary"
	MessageKindFinal      MessageKind = "final"
	MessageKindPlan       MessageKind = "plan"
	MessageKindAssistant  MessageKind = "assistant"
)

func (k MessageKind) Label() string {
	switch k {
	case MessageKindUser:
		return "あなた"
	case MessageKindCommentary:
		return "進捗報告"
	case MessageKindFinal:
		return "最終回答"
	case MessageKindPlan:
		return "計画"
	default:
		return "応答"
	}
}

// TranslationState is the per-message translation lifecycle.
type TranslationState string

const (
	TranslationStateOriginal    TranslationState = "original"
	TranslationStateSkipped     TranslationState = "skipped"
	TranslationStateQueued      TranslationState = "queued"
	TranslationStateTranslating TranslationState = "translating"
	TranslationStateTranslated  TranslationState = "translated"
	TranslationStatePartial     TranslationState = "partial"
	TranslationStateFailed      TranslationState = "failed"
)

func (s TranslationState) Label() string {
	switch s {
	case TranslationStateOriginal:
		return "未翻訳"
	case TranslationStateQueued:
		return "待機"
	case TranslationStateTranslating:
		return "翻訳中"
	case TranslationStatePartial:
		return "部分翻訳"
	case TranslationStateFailed:
		return "翻訳失敗"
	default:
		return ""
	}
}

// Message is one allowlisted transcript message.
type Message struct {
	ThreadID  string
	ID        string
	Revision  string
	Sequence  int64
	Timestamp string
	Kind      MessageKind
	Original  string
}

// SessionKey identifies a session independently of its display position.
type SessionKey struct {
	Provider Provider
	ID       string
	Path     string
}

// MessageKey identifies a message within a thread.
type MessageKey struct {
	ThreadID string
	ID       string
}

// TaskRef is the identity carried by every translation request and result.
type TaskRef struct {
	Session    SessionKey
	Generation uint64
	Message    MessageKey
	Revision   string
	UnitID     int
}

// IndexEntry is a half-open byte range in a transcript file.
type IndexEntry struct {
	ID        string
	Revision  string
	Start     int64
	End       int64
	Timestamp string
	Kind      MessageKind
}

// SessionInfo is one discovered session. Optional metadata is a pointer so a
// missing value stays distinguishable from an empty string.
type SessionInfo struct {
	ThreadID       string
	Path           string
	Cwd            *string
	UpdatedAt      float64
	HistoryMode    *string
	CLIVersion     *string
	ParentThreadID *string
	Source         *string
	Provider       Provider
	// Title is resolved deterministically from the session's own metadata or
	// first allowlisted user message. An empty value means unknown.
	Title string
}

// Key returns the stable identity of the session.
func (s SessionInfo) Key() SessionKey {
	return SessionKey{Provider: s.Provider, ID: s.ThreadID, Path: s.Path}
}

func (s SessionInfo) DisplayID() string {
	if utf8.RuneCountInString(s.ThreadID) <= 8 {
		return s.ThreadID
	}
	runes := []rune(s.ThreadID)
	return string(runes[:8]) + "…"
}

func (s SessionInfo) IsSubagent() bool { return s.ParentThreadID != nil }

func (s SessionInfo) OriginLabel() string {
	if s.ParentThreadID != nil {
		return "子セッション(親不明)"
	}
	return ""
}

func (s SessionInfo) ModeLabel() string {
	if s.HistoryMode == nil || *s.HistoryMode == "" || *s.HistoryMode == "legacy" {
		return "legacy"
	}
	switch *s.HistoryMode {
	case "claude-jsonl":
		return "claude-jsonl"
	case "paginated":
		return "paginated"
	default:
		return "未対応"
	}
}

func MakeRevision(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// FormatTimestamp renders an RFC3339 timestamp as local HH:MM:SS, or the
// explicit unknown label. A timestamp without an offset is rendered as-is.
func FormatTimestamp(timestamp string) string {
	if timestamp == "" {
		return "時刻不明"
	}
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02T15:04:05.999999999-0700",
		"2006-01-02 15:04:05.999999999-0700",
		"2006-01-02",
	}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, timestamp)
		if err != nil {
			continue
		}
		if parsed.Location() == time.UTC && !strings.HasSuffix(timestamp, "Z") && !hasNumericOffset(timestamp) {
			return parsed.Format("15:04:05")
		}
		return parsed.Local().Format("15:04:05")
	}
	return "時刻不明"
}

func hasNumericOffset(value string) bool {
	if len(value) >= 6 {
		tail := value[len(value)-6:]
		if (tail[0] == '+' || tail[0] == '-') && tail[3] == ':' {
			return true
		}
	}
	if len(value) >= 5 {
		tail := value[len(value)-5:]
		if tail[0] != '+' && tail[0] != '-' {
			return false
		}
		for _, r := range tail[1:] {
			if r < '0' || r > '9' {
				return false
			}
		}
		return true
	}
	return false
}

// String is for diagnostics that must not include message bodies.
func (m Message) String() string {
	return fmt.Sprintf("%s/%s", m.ThreadID, m.ID)
}
