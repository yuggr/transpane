package protection

import "testing"

func FuzzProtect(f *testing.F) {
	for _, seed := range []string{"", "hello", "# h\n", "- item\n", "`x`", "a\r\nb", "日本語", "@@CJ0@@", "```\nx\n```\n"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, source string) {
		doc := ProtectDocument(source)
		if got := doc.Render(nil); got != source {
			t.Fatalf("empty render changed source")
		}
		for _, unit := range doc.Units {
			if unit.SkipReason != "" && len(unit.Segments) != 0 {
				t.Fatalf("skipped unit has segments")
			}
			for _, segment := range unit.Segments {
				if len([]rune(segment.Text)) > 1000 {
					t.Fatalf("segment over limit")
				}
			}
		}
	})
}

func FuzzValidate(f *testing.F) {
	f.Add("@@CJ0@@", "@@CJ0@@")
	f.Fuzz(func(t *testing.T, text, marker string) {
		_ = ValidateSegmentTranslation(text, []string{marker})
	})
}
