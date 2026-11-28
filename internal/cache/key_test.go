package cache

import "testing"

func TestKeyPythonCompatible(t *testing.T) {
	base := "http://127.0.0.1:1234/v1"
	model := "plamo-3-translate-2b"
	cases := map[string]string{
		"hello":                "8f1899a2a426e162b566d6a00f2e975b4da89ce0a3af98a0a67edf17563db380",
		"保存 @@CJ0@@":           "50c95c221bc5efdbe23cdaa9ef3de5a957ee8ea1eb47d4f9316b07ad70d88f8e",
		"line\u2028sep\u2029x": "e605de5fd3c41ccee5f17e56c8e83cb3e04bd9f5f059eb31ee1586e04ab4d5b6",
		"quote\" back\\ slash": "653999913894470d6e7ca13eaaeb2a40dd7253db2448186d85507920431c5491",
		"ctrl\x01\x1f del\x7f": "7564222f5c2cfea77f31429759798337ad4de30d05ceaf6fa6f4cbd3f243b1a7",
		"<>&/ é 🚀":             "9741026a931493244874bc5feaaee2b34285b7b4f690eabb0f3a4dfcfc6d133c",
	}
	for segment, want := range cases {
		if got := Key(base, model, "polite", segment); got != want {
			t.Fatalf("%q: got %s want %s", segment, got, want)
		}
	}
	if Key(base, "other", "polite", "hello") == Key(base, model, "polite", "hello") ||
		Key(base, model, "plain", "hello") == Key(base, model, "polite", "hello") ||
		Key(base, model, "polite", "hello!") == Key(base, model, "polite", "hello") {
		t.Fatal("key does not separate contract values")
	}
}
