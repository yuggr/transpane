package domain

import (
	"strings"
	"testing"
	"time"
)

func TestRevisionAndLabels(t *testing.T) {
	if got := MakeRevision("hello"); got != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Fatalf("revision %s", got)
	}
	if string(ProviderCodex) != "codex" || ProviderClaude.Label() != "Claude Code" {
		t.Fatal("provider labels")
	}
	if MessageKindUser.Label() != "あなた" || MessageKindFinal.Label() != "最終回答" {
		t.Fatal("kind labels")
	}
	if TranslationStateQueued.Label() != "待機" || TranslationStateTranslated.Label() != "" {
		t.Fatal("state labels")
	}
	long := SessionInfo{ThreadID: "1234567890"}
	if long.DisplayID() != "12345678…" {
		t.Fatalf("display %s", long.DisplayID())
	}
	short := SessionInfo{ThreadID: "abc"}
	if short.DisplayID() != "abc" {
		t.Fatal("short id")
	}
}

func TestModeAndOriginLabels(t *testing.T) {
	mode := "paginated"
	if got := (SessionInfo{HistoryMode: &mode}).ModeLabel(); got != "paginated" {
		t.Fatal(got)
	}
	if got := (SessionInfo{}).ModeLabel(); got != "legacy" {
		t.Fatal(got)
	}
	if got := (SessionInfo{}).OriginLabel(); got != "" {
		t.Fatal(got)
	}
	parent := "p"
	if got := (SessionInfo{ParentThreadID: &parent}).OriginLabel(); got != "子セッション(親不明)" {
		t.Fatal(got)
	}
}

func TestFormatTimestamp(t *testing.T) {
	if got := FormatTimestamp("not a time"); got != "時刻不明" {
		t.Fatal(got)
	}
	if got := FormatTimestamp("2026-11-25T01:02:03"); got != "01:02:03" {
		t.Fatal(got)
	}
	if got := FormatTimestamp("2026-11-25"); got != "00:00:00" {
		t.Fatalf("date only: %s", got)
	}
	if got := FormatTimestamp("2026-11-25T01:02:03+0900"); len(got) != 8 {
		t.Fatalf("compact offset: %s", got)
	}
	got := FormatTimestamp("2026-11-25T01:02:03Z")
	if len(got) != 8 || strings.Count(got, ":") != 2 {
		t.Fatalf("local time %q", got)
	}
	_ = time.Now
}
