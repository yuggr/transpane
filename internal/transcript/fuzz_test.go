package transcript

import "testing"

func FuzzDecodeObject(f *testing.F) {
	for _, seed := range []string{"{}", `{"type":"event_msg"}`, "{", "[]", "\xff", `{"a":1} trailing`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = DecodeObject(raw)
	})
}

func FuzzLineSplitter(f *testing.F) {
	f.Add([]byte("a\nb\n"), []byte(""))
	f.Add([]byte("incomplete"), []byte(" tail\n"))
	f.Fuzz(func(t *testing.T, first, second []byte) {
		splitter := newLineSplitter(0, 1024)
		lines, oversized := splitter.feed(first)
		if oversized < 0 {
			t.Fatal("negative oversized")
		}
		lines2, oversized2 := splitter.feed(second)
		lines = append(lines, lines2...)
		if oversized2 < 0 {
			t.Fatal("negative oversized")
		}
		var previous int64 = -1
		for _, line := range lines {
			if line.offset <= previous {
				t.Fatalf("offsets are not increasing")
			}
			previous = line.offset
			if int64(len(line.raw)) > int64(1024) {
				t.Fatalf("line over limit")
			}
		}
		if splitter.consumed() < 0 {
			t.Fatal("negative consumed")
		}
	})
}
