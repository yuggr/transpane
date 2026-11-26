package transcript

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/yuggr/transpane/internal/domain"
)

// HistoryMode is the supported Codex rollout history shape.
type HistoryMode string

const (
	HistoryModeLegacy    HistoryMode = "legacy"
	HistoryModePaginated HistoryMode = "paginated"
)

// ParseHistoryMode maps the metadata value to a supported mode. Absent, null
// and empty values are legacy.
func ParseHistoryMode(value any) (HistoryMode, error) {
	if value == nil {
		return HistoryModeLegacy, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("未対応の history_mode: '%v'", value)
	}
	switch text {
	case "", "legacy":
		return HistoryModeLegacy, nil
	case "paginated":
		return HistoryModePaginated, nil
	default:
		return "", fmt.Errorf("未対応の history_mode: '%s'", text)
	}
}

// ExtractResult is the allowlist decision for one record.
type ExtractResult struct {
	Message *domain.Message
	Reason  string
}

func rejected(reason string) ExtractResult { return ExtractResult{Reason: reason} }

func asMap(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

func asList(value any) []any {
	list, _ := value.([]any)
	return list
}

func asString(value any) (string, bool) {
	text, ok := value.(string)
	return text, ok
}

func kindForPhase(phase any) domain.MessageKind {
	switch phase {
	case "commentary":
		return domain.MessageKindCommentary
	case "final_answer":
		return domain.MessageKindFinal
	default:
		return domain.MessageKindAssistant
	}
}

func recordTimestamp(record map[string]any) string {
	text, _ := asString(record["timestamp"])
	return text
}

func joinContent(content any, elementType string) string {
	var builder strings.Builder
	for _, element := range asList(content) {
		object := asMap(element)
		if object == nil {
			continue
		}
		if object["type"] != elementType {
			continue
		}
		if text, ok := asString(object["text"]); ok {
			builder.WriteString(text)
		}
	}
	return builder.String()
}

// Extractor converts one Codex session's records.
type Extractor struct {
	ThreadID string
	Mode     HistoryMode
}

func NewExtractor(threadID string, mode HistoryMode) *Extractor {
	return &Extractor{ThreadID: threadID, Mode: mode}
}

func (e *Extractor) Extract(record map[string]any, offset int64) ExtractResult {
	if record["type"] != "event_msg" {
		return rejected("not_allowed")
	}
	return e.extractEventMsg(asMap(record["payload"]), record, offset)
}

func (e *Extractor) extractEventMsg(payload map[string]any, record map[string]any, offset int64) ExtractResult {
	if payload == nil {
		return rejected("not_allowed")
	}
	payloadType, _ := asString(payload["type"])
	if payloadType == "item_completed" {
		if item := asMap(payload["item"]); item != nil {
			if result, handled := e.extractItem(item, record, offset); handled {
				return result
			}
		}
		return rejected("not_allowed")
	}
	if e.Mode == HistoryModeLegacy {
		if payloadType == "user_message" {
			return e.fromText(payload["message"], domain.MessageKindUser, record, offset)
		}
		if payloadType == "agent_message" {
			return e.fromText(payload["message"], kindForPhase(payload["phase"]), record, offset)
		}
	}
	return rejected("not_allowed")
}

func (e *Extractor) extractItem(item map[string]any, record map[string]any, offset int64) (ExtractResult, bool) {
	if item["type"] == "Plan" {
		text, ok := asString(item["text"])
		if !ok || strings.TrimSpace(text) == "" {
			return rejected("empty"), true
		}
		messageID, ok := asString(item["id"])
		if !ok || messageID == "" {
			messageID = e.ThreadID + ":" + strconv.FormatInt(offset, 10)
		}
		return ExtractResult{Message: e.build(messageID, text, domain.MessageKindPlan, record, offset)}, true
	}
	if e.Mode != HistoryModePaginated {
		return ExtractResult{}, false
	}
	var text string
	var kind domain.MessageKind
	switch item["type"] {
	case "UserMessage":
		text = joinContent(item["content"], "text")
		kind = domain.MessageKindUser
	case "AgentMessage":
		text = joinContent(item["content"], "Text")
		kind = kindForPhase(item["phase"])
	default:
		return ExtractResult{}, false
	}
	messageID, ok := asString(item["id"])
	if !ok || messageID == "" {
		return rejected("missing_id"), true
	}
	if strings.TrimSpace(text) == "" {
		return rejected("empty"), true
	}
	return ExtractResult{Message: e.build(messageID, text, kind, record, offset)}, true
}

func (e *Extractor) fromText(value any, kind domain.MessageKind, record map[string]any, offset int64) ExtractResult {
	text, ok := asString(value)
	if !ok {
		return rejected("bad_payload")
	}
	if strings.TrimSpace(text) == "" {
		return rejected("empty")
	}
	messageID := e.ThreadID + ":" + strconv.FormatInt(offset, 10)
	return ExtractResult{Message: e.build(messageID, text, kind, record, offset)}
}

func (e *Extractor) build(messageID, text string, kind domain.MessageKind, record map[string]any, offset int64) *domain.Message {
	return &domain.Message{
		ThreadID:  e.ThreadID,
		ID:        messageID,
		Revision:  domain.MakeRevision(text),
		Sequence:  offset,
		Timestamp: recordTimestamp(record),
		Kind:      kind,
		Original:  text,
	}
}

var claudeFlags = []string{
	"isSidechain", "isMeta", "isCompactSummary", "isVisibleInTranscriptOnly", "isApiErrorMessage",
}

var claudeInternalTags = []string{
	"<system-reminder>", "<local-command-caveat>", "<local-command-stdout>",
	"<local-command-stderr>", "<command-message>", "<command-name>", "<command-args>",
	"<task-notification>", "<persisted-output>", "<bash-stdout>",
}

var claudeHiddenAssistantBlocks = map[string]bool{
	"thinking": true, "redacted_thinking": true, "tool_use": true,
}

var claudeIgnoredUserBlocks = map[string]bool{"image": true}

const claudeSyntheticModel = "<synthetic>"

// ClaudeExtractor converts one Claude Code transcript.
type ClaudeExtractor struct{ ThreadID string }

func NewClaudeExtractor(threadID string) *ClaudeExtractor {
	return &ClaudeExtractor{ThreadID: threadID}
}

func (e *ClaudeExtractor) Extract(record map[string]any, offset int64) ExtractResult {
	message, reason := e.validate(record)
	if reason != "" {
		return rejected(reason)
	}
	recordType, _ := asString(record["type"])
	text, ok := claudeContentText(recordType, message["content"])
	if !ok {
		return rejected("excluded")
	}
	if strings.TrimSpace(text) == "" {
		return rejected("empty")
	}
	messageID, _ := asString(record["uuid"])
	kind := domain.MessageKindAssistant
	if recordType == "user" {
		kind = domain.MessageKindUser
	}
	return ExtractResult{Message: &domain.Message{
		ThreadID:  e.ThreadID,
		ID:        messageID,
		Revision:  domain.MakeRevision(text),
		Sequence:  offset,
		Timestamp: recordTimestamp(record),
		Kind:      kind,
		Original:  text,
	}}
}

func (e *ClaudeExtractor) validate(record map[string]any) (map[string]any, string) {
	recordType, _ := asString(record["type"])
	if recordType != "user" && recordType != "assistant" {
		return nil, "not_allowed"
	}
	for _, name := range claudeFlags {
		value, present := record[name]
		if !present {
			continue
		}
		flag, isBool := value.(bool)
		if !isBool || flag {
			return nil, "excluded"
		}
	}
	sessionID, ok := asString(record["sessionId"])
	if !ok || sessionID == "" {
		return nil, "not_allowed"
	}
	if sessionID != e.ThreadID {
		return nil, "thread_mismatch"
	}
	if uuid, ok := asString(record["uuid"]); !ok || uuid == "" {
		return nil, "missing_id"
	}
	message := asMap(record["message"])
	if message == nil {
		return nil, "bad_payload"
	}
	if role, _ := asString(message["role"]); role != recordType {
		return nil, "role_mismatch"
	}
	if model, _ := asString(message["model"]); model == claudeSyntheticModel {
		return nil, "synthetic"
	}
	if recordType == "user" {
		if value, present := record["toolUseResult"]; present && value != nil {
			return nil, "excluded"
		}
	}
	return message, ""
}

func claudeContentText(recordType string, content any) (string, bool) {
	var texts []string
	switch {
	case content == nil:
		return "", false
	case isString(content):
		if recordType != "user" {
			return "", false
		}
		texts = []string{content.(string)}
	case isList(content):
		ignored := claudeHiddenAssistantBlocks
		if recordType == "user" {
			ignored = claudeIgnoredUserBlocks
		}
		for _, raw := range content.([]any) {
			block := asMap(raw)
			if block == nil {
				return "", false
			}
			blockType, _ := asString(block["type"])
			if blockType == "text" {
				text, ok := asString(block["text"])
				if !ok {
					return "", false
				}
				if strings.TrimSpace(text) != "" {
					texts = append(texts, text)
				}
				continue
			}
			if !ignored[blockType] {
				return "", false
			}
		}
	default:
		return "", false
	}
	combined := strings.Join(texts, "")
	for _, tag := range claudeInternalTags {
		if strings.Contains(combined, tag) {
			return "", false
		}
	}
	return strings.Join(texts, "\n\n"), true
}

func isString(value any) bool { _, ok := value.(string); return ok }
func isList(value any) bool   { _, ok := value.([]any); return ok }
