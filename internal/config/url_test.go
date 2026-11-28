package config

import "testing"

func TestValidURLs(t *testing.T) {
	cases := map[string]string{
		"http://127.0.0.1:1234/v1":    "http://127.0.0.1:1234/v1",
		"http://127.0.0.1/v1":         "http://127.0.0.1:80/v1",
		"http://[::1]:8080/v1":        "http://[::1]:8080/v1",
		"http://10.1.2.3:1234/v1":     "http://10.1.2.3:1234/v1",
		"http://172.16.0.9:1234/v1":   "http://172.16.0.9:1234/v1",
		"http://192.168.0.20:1234/v1": "http://192.168.0.20:1234/v1",
		"http://127.0.0.1:1234/v1/":   "http://127.0.0.1:1234/v1",
	}
	for in, want := range cases {
		got, err := ValidateBaseURL(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got != want {
			t.Fatalf("%s: got %s want %s", in, got, want)
		}
	}
}

func TestRejectedURLs(t *testing.T) {
	for _, in := range []string{
		"https://127.0.0.1:1234/v1",
		"http://localhost:1234/v1",
		"http://8.8.8.8/v1",
		"http://172.32.0.1/v1",
		"http://192.169.0.1/v1",
		"http://user:pass@127.0.0.1:1234/v1",
		"http://127.0.0.1:1234/v1?x=1",
		"http://127.0.0.1:1234/v1#frag",
		"http://127.0.0.1:1234/v2",
		"http://127.0.0.1:1234/v1/extra",
		"http://[fe80::1]:1234/v1",
		"ftp://127.0.0.1/v1",
		"",
		"http://127.0.0.1:0/v1",
		"http://[::ffff:127.0.0.1]:1234/v1",
		"http://[fe80::1%25eth0]:1234/v1",
	} {
		if got, err := ValidateBaseURL(in); err == nil {
			t.Fatalf("%s accepted as %s", in, got)
		}
	}
}

func FuzzValidateBaseURL(f *testing.F) {
	for _, seed := range []string{"http://127.0.0.1:1234/v1", "http://[::1]:80/v1", "http://10.0.0.1/v1", "https://x/v1", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		normalized, err := ValidateBaseURL(raw)
		if err != nil {
			return
		}
		again, err := ValidateBaseURL(normalized)
		if err != nil || again != normalized {
			t.Fatalf("normalization is not idempotent: %q -> %q -> %q (%v)", raw, normalized, again, err)
		}
	})
}
