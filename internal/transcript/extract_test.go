package transcript

import (
	"strings"
	"testing"

	"github.com/yuggr/transpane/internal/domain"
)

func TestLegacyExtraction(t *testing.T) {
	extractor := NewExtractor("legacy-thread", HistoryModeLegacy)
	user := extractor.Extract(map[string]any{
		"timestamp": "2026-11-25T01:00:01Z", "type": "event_msg",
		"payload": map[string]any{"type": "user_message", "message": "保存処理を調べて"},
	}, 10)
	if user.Message == nil || user.Message.Kind != domain.MessageKindUser || user.Message.Sequence != 10 || user.Message.Timestamp != "2026-11-25T01:00:01Z" {
		t.Fatalf("%+v", user)
	}
	agent := extractor.Extract(map[string]any{"type": "event_msg", "payload": map[string]any{"type": "agent_message", "message": "I will inspect.", "phase": "commentary"}}, 20)
	if agent.Message == nil || agent.Message.Kind != domain.MessageKindCommentary {
		t.Fatalf("%+v", agent)
	}
	response := extractor.Extract(map[string]any{"type": "response_item", "payload": map[string]any{"type": "message"}}, 30)
	if response.Message != nil || response.Reason != "not_allowed" {
		t.Fatalf("%+v", response)
	}
}

func TestPaginatedAndPhases(t *testing.T) {
	extractor := NewExtractor("new-thread", HistoryModePaginated)
	user := extractor.Extract(map[string]any{"type": "event_msg", "payload": map[string]any{
		"type": "item_completed", "item": map[string]any{"type": "UserMessage", "id": "u1", "content": []any{
			map[string]any{"type": "text", "text": "保存処理を"}, map[string]any{"type": "text", "text": "調べて"},
		}},
	}}, 40)
	if user.Message == nil || user.Message.ID != "u1" || user.Message.Original != "保存処理を調べて" {
		t.Fatalf("%+v", user)
	}
	for phase, want := range map[string]domain.MessageKind{"commentary": domain.MessageKindCommentary, "final_answer": domain.MessageKindFinal, "other": domain.MessageKindAssistant} {
		result := extractor.Extract(map[string]any{"type": "event_msg", "payload": map[string]any{
			"type": "item_completed", "item": map[string]any{"type": "AgentMessage", "id": "a-" + phase, "phase": phase, "content": []any{map[string]any{"type": "Text", "text": "Done."}}},
		}}, 50)
		if result.Message == nil || result.Message.Kind != want {
			t.Fatalf("%s: %+v", phase, result)
		}
	}
}

func TestPlanAndStrictModes(t *testing.T) {
	for _, mode := range []HistoryMode{HistoryModeLegacy, HistoryModePaginated} {
		result := NewExtractor("t", mode).Extract(map[string]any{"type": "event_msg", "payload": map[string]any{
			"type": "item_completed", "item": map[string]any{"type": "Plan", "id": "plan-1", "text": "- step one\n- step two"},
		}}, 60)
		if result.Message == nil || result.Message.Kind != domain.MessageKindPlan || result.Message.ID != "plan-1" {
			t.Fatalf("%s %+v", mode, result)
		}
		result = NewExtractor("t", mode).Extract(map[string]any{"type": "event_msg", "payload": map[string]any{
			"type": "item_completed", "item": map[string]any{"type": "Plan", "text": "Step one"},
		}}, 70)
		if result.Message == nil || result.Message.ID != "t:70" {
			t.Fatalf("%+v", result)
		}
	}
	if legacy := NewExtractor("t", HistoryModeLegacy).Extract(map[string]any{"type": "event_msg", "payload": map[string]any{
		"type": "item_completed", "item": map[string]any{"type": "AgentMessage", "id": "a1", "content": []any{map[string]any{"type": "Text", "text": "x"}}},
	}}, 0); legacy.Message != nil {
		t.Fatal("legacy accepted paginated item")
	}
	if paginated := NewExtractor("t", HistoryModePaginated).Extract(map[string]any{"type": "event_msg", "payload": map[string]any{"type": "agent_message", "message": "x"}}, 0); paginated.Message != nil {
		t.Fatal("paginated accepted legacy event")
	}
}

func TestMissingIDAndEmpty(t *testing.T) {
	extractor := NewExtractor("t", HistoryModePaginated)
	result := extractor.Extract(map[string]any{"type": "event_msg", "payload": map[string]any{
		"type": "item_completed", "item": map[string]any{"type": "AgentMessage", "content": []any{map[string]any{"type": "Text", "text": "x"}}},
	}}, 0)
	if result.Message != nil || result.Reason != "missing_id" {
		t.Fatalf("%+v", result)
	}
	result = extractor.Extract(map[string]any{"type": "event_msg", "payload": map[string]any{
		"type": "item_completed", "item": map[string]any{"type": "AgentMessage", "id": "a", "content": []any{map[string]any{"type": "Text", "text": "  "}}},
	}}, 0)
	if result.Message != nil || result.Reason != "empty" {
		t.Fatalf("%+v", result)
	}
}

func TestHistoryModeParsing(t *testing.T) {
	for _, value := range []any{nil, "", "legacy"} {
		mode, err := ParseHistoryMode(value)
		if err != nil || mode != HistoryModeLegacy {
			t.Fatalf("%v: %v %v", value, mode, err)
		}
	}
	mode, err := ParseHistoryMode("paginated")
	if err != nil || mode != HistoryModePaginated {
		t.Fatal(mode, err)
	}
	if _, err := ParseHistoryMode("chunked"); err == nil || !strings.Contains(err.Error(), "'chunked'") {
		t.Fatalf("err %v", err)
	}
}

func TestRevision(t *testing.T) {
	extractor := NewExtractor("t", HistoryModePaginated)
	record := map[string]any{"type": "event_msg", "payload": map[string]any{"type": "item_completed", "item": map[string]any{
		"type": "AgentMessage", "id": "a", "content": []any{map[string]any{"type": "Text", "text": "hello"}},
	}}}
	first := extractor.Extract(record, 0).Message
	second := extractor.Extract(record, 0).Message
	if first == nil || second == nil || first.Revision != second.Revision || len(first.Revision) != 64 {
		t.Fatal("revision")
	}
}

// Claude extractor tests

func claudeTestRecord(kind string, content any, extra map[string]any) map[string]any {
	record := map[string]any{
		"type": kind, "sessionId": claudeSession, "uuid": "00000000-0000-4000-8000-000000000010",
		"timestamp": "2026-11-25T03:00:00Z",
		"message":   map[string]any{"role": kind, "content": content},
	}
	for key, value := range extra {
		record[key] = value
	}
	return record
}

func TestClaudeExtraction(t *testing.T) {
	result := NewClaudeExtractor(claudeSession).Extract(claudeTestRecord("user", "保存処理を確認して", nil), 10)
	if result.Message == nil || result.Message.Kind != domain.MessageKindUser || result.Message.ID != "00000000-0000-4000-8000-000000000010" {
		t.Fatalf("%+v", result)
	}
	blocks := NewClaudeExtractor(claudeSession).Extract(claudeTestRecord("user", []any{
		map[string]any{"type": "text", "text": "first line"},
		map[string]any{"type": "image", "source": "x"},
		map[string]any{"type": "text", "text": "second line"},
	}, nil), 0)
	if blocks.Message == nil || blocks.Message.Original != "first line\n\nsecond line" {
		t.Fatalf("%+v", blocks)
	}
	assistant := NewClaudeExtractor(claudeSession).Extract(claudeTestRecord("assistant", []any{
		map[string]any{"type": "thinking", "thinking": "DO_NOT_SEND"},
		map[string]any{"type": "redacted_thinking", "data": "DO_NOT_SEND"},
		map[string]any{"type": "tool_use", "id": "t"},
		map[string]any{"type": "text", "text": "I will inspect."},
		map[string]any{"type": "text", "text": "The tests passed."},
	}, nil), 0)
	if assistant.Message == nil || assistant.Message.Original != "I will inspect.\n\nThe tests passed." {
		t.Fatalf("%+v", assistant)
	}
}

func TestClaudeUnverifiedShapes(t *testing.T) {
	cases := []struct {
		kind    string
		content any
	}{
		{"user", []any{map[string]any{"type": "text", "text": "keep"}, map[string]any{"type": "tool_result", "content": "drop"}}},
		{"user", []any{map[string]any{"type": "unknown", "text": "drop"}}},
		{"user", []any{map[string]any{"type": "text", "text": "drop"}, "not-an-object"}},
		{"assistant", []any{map[string]any{"type": "text", "text": "drop"}, map[string]any{"type": "unknown"}}},
		{"assistant", "a string"},
		{"assistant", []any{map[string]any{"type": "text", "text": "  "}}},
		{"user", ""},
		{"user", []any{}},
	}
	for _, item := range cases {
		result := NewClaudeExtractor(claudeSession).Extract(claudeTestRecord(item.kind, item.content, nil), 0)
		if result.Message != nil {
			t.Fatalf("adopted %+v", item)
		}
	}
}

func TestClaudeFlagsAndPriorities(t *testing.T) {
	for _, flag := range []string{"isSidechain", "isMeta", "isCompactSummary", "isVisibleInTranscriptOnly", "isApiErrorMessage"} {
		if result := NewClaudeExtractor(claudeSession).Extract(claudeTestRecord("user", "hidden", map[string]any{flag: true}), 0); result.Message != nil {
			t.Fatalf("%s true adopted", flag)
		}
		if result := NewClaudeExtractor(claudeSession).Extract(claudeTestRecord("user", "shown", map[string]any{flag: false}), 0); result.Message == nil {
			t.Fatalf("%s false rejected", flag)
		}
		if result := NewClaudeExtractor(claudeSession).Extract(claudeTestRecord("user", "hidden", map[string]any{flag: "true"}), 0); result.Message != nil {
			t.Fatalf("%s string adopted", flag)
		}
	}
	result := NewClaudeExtractor(claudeSession).Extract(claudeTestRecord("user", "hidden", map[string]any{"sessionId": "other", "isSidechain": true}), 0)
	if result.Reason != "excluded" {
		t.Fatalf("priority %+v", result)
	}
	result = NewClaudeExtractor(claudeSession).Extract(claudeTestRecord("user", "visible", map[string]any{"sessionId": "other"}), 0)
	if result.Reason != "thread_mismatch" {
		t.Fatalf("mismatch %+v", result)
	}
}

func TestClaudeToolAndSynthetic(t *testing.T) {
	if result := NewClaudeExtractor(claudeSession).Extract(claudeTestRecord("user", "drop", map[string]any{"toolUseResult": map[string]any{"stdout": "x"}}), 0); result.Message != nil {
		t.Fatal("tool result adopted")
	}
	if result := NewClaudeExtractor(claudeSession).Extract(claudeTestRecord("user", "keep", map[string]any{"toolUseResult": nil}), 0); result.Message == nil {
		t.Fatal("nil tool result rejected")
	}
	synthetic := claudeTestRecord("assistant", []any{map[string]any{"type": "text", "text": "x"}}, nil)
	synthetic["message"].(map[string]any)["model"] = "<synthetic>"
	if result := NewClaudeExtractor(claudeSession).Extract(synthetic, 0); result.Message != nil {
		t.Fatal("synthetic adopted")
	}
	wrongRole := claudeTestRecord("user", "x", nil)
	wrongRole["message"].(map[string]any)["role"] = "assistant"
	if result := NewClaudeExtractor(claudeSession).Extract(wrongRole, 0); result.Message != nil {
		t.Fatal("wrong role adopted")
	}
	emptyUUID := claudeTestRecord("user", "x", map[string]any{"uuid": ""})
	if result := NewClaudeExtractor(claudeSession).Extract(emptyUUID, 0); result.Reason != "missing_id" {
		t.Fatalf("%+v", result)
	}
}

func TestClaudeInternalTags(t *testing.T) {
	tags := []string{"<system-reminder>", "<local-command-caveat>", "<local-command-stdout>", "<local-command-stderr>", "<command-message>", "<command-name>", "<command-args>", "<task-notification>", "<persisted-output>", "<bash-stdout>"}
	for _, tag := range tags {
		text := tag + " internal"
		if result := NewClaudeExtractor(claudeSession).Extract(claudeTestRecord("user", text, nil), 0); result.Message != nil {
			t.Fatalf("user %s adopted", tag)
		}
		if result := NewClaudeExtractor(claudeSession).Extract(claudeTestRecord("assistant", []any{map[string]any{"type": "text", "text": text}}, nil), 0); result.Message != nil {
			t.Fatalf("assistant %s adopted", tag)
		}
	}
	split := NewClaudeExtractor(claudeSession).Extract(claudeTestRecord("user", []any{
		map[string]any{"type": "text", "text": "<system-"},
		map[string]any{"type": "text", "text": "reminder> hidden"},
	}, nil), 0)
	if split.Message != nil || split.Reason != "excluded" {
		t.Fatalf("%+v", split)
	}
}

func TestClaudeSameMessageIDDifferentUUID(t *testing.T) {
	first := claudeTestRecord("assistant", []any{map[string]any{"type": "text", "text": "first"}}, map[string]any{"uuid": "u-1"})
	second := claudeTestRecord("assistant", []any{map[string]any{"type": "text", "text": "second"}}, map[string]any{"uuid": "u-2"})
	first["message"].(map[string]any)["id"] = "msg_a"
	second["message"].(map[string]any)["id"] = "msg_a"
	one := NewClaudeExtractor(claudeSession).Extract(first, 0)
	two := NewClaudeExtractor(claudeSession).Extract(second, 0)
	if one.Message == nil || two.Message == nil || one.Message.ID != "u-1" || two.Message.ID != "u-2" {
		t.Fatalf("%+v %+v", one, two)
	}
}
