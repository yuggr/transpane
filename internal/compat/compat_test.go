package compat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yuggr/transpane/internal/cache"
	"github.com/yuggr/transpane/internal/domain"
	"github.com/yuggr/transpane/internal/protection"
	"github.com/yuggr/transpane/internal/transcript"
)

type messageJSON struct {
	ID        string `json:"id"`
	Revision  string `json:"revision"`
	Kind      string `json:"kind"`
	Offset    int64  `json:"offset"`
	Text      string `json:"text"`
	Timestamp string `json:"timestamp"`
}

type entryJSON struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
	Offset   int64  `json:"offset"`
	End      int64  `json:"end"`
	Kind     string `json:"kind"`
}

type statsJSON struct {
	Records        int `json:"records"`
	Events         int `json:"events"`
	Extracted      int `json:"extracted"`
	Ignored        int `json:"ignored"`
	InvalidLines   int `json:"invalid_lines"`
	OversizedLines int `json:"oversized_lines"`
}

type scanJSON struct {
	HistoryMode   *string       `json:"history_mode"`
	Unsupported   *string       `json:"unsupported"`
	ThreadChanged *string       `json:"thread_changed"`
	Stats         statsJSON     `json:"stats"`
	Entries       []entryJSON   `json:"entries"`
	Messages      []messageJSON `json:"messages"`
}

type segmentJSON struct {
	Text     string   `json:"text"`
	Markers  []string `json:"markers"`
	Trailing string   `json:"trailing"`
}

type unitJSON struct {
	ID           int           `json:"id"`
	Line         int           `json:"line"`
	Prefix       string        `json:"prefix"`
	Suffix       string        `json:"suffix"`
	Source       string        `json:"source"`
	Markers      []string      `json:"markers"`
	Skip         *string       `json:"skip"`
	Conservative bool          `json:"conservative"`
	Segments     []segmentJSON `json:"segments"`
}

type protectionJSON struct {
	Units    []unitJSON `json:"units"`
	Rendered string     `json:"rendered"`
}

type goldenJSON struct {
	Codex      scanJSON          `json:"codex"`
	Claude     scanJSON          `json:"claude"`
	Protection protectionJSON    `json:"protection"`
	CacheKeys  map[string]string `json:"cache_keys"`
}

func loadGolden(t *testing.T) goldenJSON {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "compat", "golden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var golden goldenJSON
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	return golden
}

func scanFixture(t *testing.T, name, thread string, provider domain.Provider) scanJSON {
	t.Helper()
	root := filepath.Join("..", "..", "testdata", "compat")
	reader := transcript.NewReader(filepath.Join(root, name), thread, transcript.ReaderOptions{Provider: provider, AllowedRoot: root})
	result, err := reader.ScanAll()
	if err != nil {
		t.Fatal(err)
	}
	messages, err := reader.ReadMessages(result.Entries)
	if err != nil {
		t.Fatal(err)
	}
	output := scanJSON{
		HistoryMode:   result.HistoryMode,
		Unsupported:   result.UnsupportedMode,
		ThreadChanged: result.ThreadChanged,
		Stats: statsJSON{
			Records:        result.Stats.Records,
			Events:         result.Stats.Events,
			Extracted:      result.Stats.Extracted,
			Ignored:        result.Stats.Ignored,
			InvalidLines:   result.Stats.InvalidLines,
			OversizedLines: result.Stats.OversizedLines,
		},
		Entries:  []entryJSON{},
		Messages: []messageJSON{},
	}
	for _, entry := range result.Entries {
		output.Entries = append(output.Entries, entryJSON{ID: entry.ID, Revision: entry.Revision, Offset: entry.Start, End: entry.End, Kind: string(entry.Kind)})
	}
	for _, message := range messages {
		output.Messages = append(output.Messages, messageJSON{ID: message.ID, Revision: message.Revision, Kind: string(message.Kind), Offset: message.Sequence, Text: message.Original, Timestamp: message.Timestamp})
	}
	return output
}

func protectionFixture(t *testing.T) protectionJSON {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "compat", "markdown.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := protection.ProtectDocument(string(raw))
	output := protectionJSON{Units: []unitJSON{}, Rendered: doc.Render(nil)}
	for _, unit := range doc.Units {
		segments := []segmentJSON{}
		for _, segment := range unit.Segments {
			markers := segment.Markers
			if markers == nil {
				markers = []string{}
			}
			segments = append(segments, segmentJSON{Text: segment.Text, Markers: markers, Trailing: segment.Trailing})
		}
		markers := unit.Markers
		if markers == nil {
			markers = []string{}
		}
		var skip *string
		if unit.SkipReason != "" {
			value := unit.SkipReason
			skip = &value
		}
		output.Units = append(output.Units, unitJSON{
			ID: unit.ID, Line: unit.LineIndex, Prefix: unit.Prefix, Suffix: unit.Suffix,
			Source: unit.SourceText, Markers: markers, Skip: skip, Conservative: unit.Conservative, Segments: segments,
		})
	}
	return output
}

func TestSyntheticGolden(t *testing.T) {
	golden := loadGolden(t)
	codex := scanFixture(t, "codex_paginated.jsonl", "compat-codex", domain.ProviderCodex)
	if !reflect.DeepEqual(codex, golden.Codex) {
		t.Fatalf("codex mismatch\n got: %+v\nwant: %+v", codex, golden.Codex)
	}
	claude := scanFixture(t, "11111111-1111-4111-8111-111111111111.jsonl", "11111111-1111-4111-8111-111111111111", domain.ProviderClaude)
	if !reflect.DeepEqual(claude, golden.Claude) {
		t.Fatalf("claude mismatch\n got: %+v\nwant: %+v", claude, golden.Claude)
	}
	protectionOutput := protectionFixture(t)
	if !reflect.DeepEqual(protectionOutput, golden.Protection) {
		t.Fatalf("protection mismatch\n got: %+v\nwant: %+v", protectionOutput, golden.Protection)
	}
	for name, want := range golden.CacheKeys {
		segment := map[string]string{"hello": "hello", "japanese": "保存 @@CJ0@@"}[name]
		if got := cache.Key("http://127.0.0.1:1234/v1", "plamo-3-translate-2b", "polite", segment); got != want {
			t.Fatalf("%s: got %s want %s", name, got, want)
		}
	}
}
