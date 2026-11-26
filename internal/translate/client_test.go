package translate

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newClient(t *testing.T, url string, opts ...Option) *Client {
	t.Helper()
	return NewClient(url, "plamo-3-translate-2b", "polite", opts...)
}

func TestTranslateRequestAndResponse(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"保存層にはアトミック書き込みが必要です。"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	client := newClient(t, server.URL+"/v1")
	defer func() { _ = client.Close() }()
	translated, err := client.Translate(context.Background(), "The storage layer requires atomic writes.")
	if err != nil {
		t.Fatal(err)
	}
	if translated != "保存層にはアトミック書き込みが必要です。" {
		t.Fatalf("%q", translated)
	}
	if captured["model"] != "plamo-3-translate-2b" || captured["stream"] != false {
		t.Fatalf("%+v", captured)
	}
	messages := captured["messages"].([]any)
	system := messages[0].(map[string]any)
	if system["role"] != "system" || system["content"] != "[plamo-translate]\nsource_lang=en\ntarget_lang=ja\nform=polite" {
		t.Fatalf("%+v", system)
	}
	user := messages[1].(map[string]any)
	if user["role"] != "user" || user["content"] != "The storage layer requires atomic writes." {
		t.Fatalf("%+v", user)
	}
}

func TestFailureClassification(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   ErrorKind
	}{
		{"500", 500, "{}", ErrorKindHTTP},
		{"404", 404, "{}", ErrorKindHTTP},
		{"notjson", 200, "not json", ErrorKindInvalid},
		{"emptychoices", 200, `{"choices":[]}`, ErrorKindInvalid},
		{"empty", 200, `{"choices":[{"message":{"content":"  "},"finish_reason":"stop"}]}`, ErrorKindEmpty},
		{"incomplete", 200, `{"choices":[{"message":{"content":"done"},"finish_reason":"length"}]}`, ErrorKindIncomplete},
		{"badcontent", 200, `{"choices":[{"message":{"content":3},"finish_reason":"stop"}]}`, ErrorKindInvalid},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(item.status)
				_, _ = w.Write([]byte(item.body))
			}))
			defer server.Close()
			client := newClient(t, server.URL+"/v1")
			defer func() { _ = client.Close() }()
			_, err := client.Translate(context.Background(), "Hello")
			var failure *Failure
			if !errors.As(err, &failure) {
				t.Fatalf("not a Failure: %v", err)
			}
			if failure.Kind != item.want {
				t.Fatalf("got %s want %s", failure.Kind, item.want)
			}
		})
	}
}

func TestConnectAndTimeout(t *testing.T) {
	listener, listenErr := net.Listen("tcp", "127.0.0.1:0")
	if listenErr != nil {
		t.Fatal(listenErr)
	}
	closedURL := "http://" + listener.Addr().String() + "/v1"
	_ = listener.Close()
	client := NewClient(closedURL, "m", "polite", WithTimeouts(500*time.Millisecond, 500*time.Millisecond, 1*time.Second))
	_, err := client.Translate(context.Background(), "Hello")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != ErrorKindConnect || !failure.ServerUnavailable() {
		t.Fatalf("%v", err)
	}
	_ = client.Close()

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"x"},"finish_reason":"stop"}]}`))
	}))
	defer slow.Close()
	timeoutClient := NewClient(slow.URL+"/v1", "m", "polite", WithTimeouts(50*time.Millisecond, 50*time.Millisecond, 80*time.Millisecond))
	defer func() { _ = timeoutClient.Close() }()
	_, err = timeoutClient.Translate(context.Background(), "Hello")
	if !errors.As(err, &failure) || failure.Kind != ErrorKindTimeout || !failure.ServerUnavailable() {
		t.Fatalf("%v", err)
	}
}

func TestRedirectNotFollowed(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Redirect(w, r, "http://example.com/evil", http.StatusFound)
	}))
	defer server.Close()
	client := newClient(t, server.URL+"/v1")
	defer func() { _ = client.Close() }()
	_, err := client.Translate(context.Background(), "Hello")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != ErrorKindHTTP || calls != 1 {
		t.Fatalf("%v calls=%d", err, calls)
	}
}

func TestProxyEnvironmentIgnored(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")
	t.Setenv("ALL_PROXY", "http://127.0.0.1:9")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"訳"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	client := newClient(t, server.URL+"/v1")
	defer func() { _ = client.Close() }()
	if got, err := client.Translate(context.Background(), "Hello"); err != nil || got != "訳" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestListModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Method != http.MethodGet {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"a"},{"id":3},{"nope":1},{"id":"b"}]}`))
	}))
	defer server.Close()
	client := newClient(t, server.URL+"/v1")
	defer func() { _ = client.Close() }()
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(models, ",") != "a,b" {
		t.Fatalf("%v", models)
	}
}

func TestResponseBodyCap(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":"` + strings.Repeat("x", 4*1024*1024+10) + `"}`))
	}))
	defer server.Close()
	client := newClient(t, server.URL+"/v1")
	defer func() { _ = client.Close() }()
	_, err := client.Translate(context.Background(), "Hello")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != ErrorKindInvalid {
		t.Fatalf("%v", err)
	}
}

func TestCancelledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer server.Close()
	client := newClient(t, server.URL+"/v1")
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.Translate(ctx, "Hello")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Kind != ErrorKindCancelled {
		t.Fatalf("%v", err)
	}
}

func TestFailureLabelsAndServerUnavailable(t *testing.T) {
	if ErrorKindConnect.Label() != "接続失敗" || ErrorKindInvalid.Label() != "応答形式不正" {
		t.Fatal("labels")
	}
	if !(&Failure{Kind: ErrorKindHTTP, Status: 503}).ServerUnavailable() ||
		(&Failure{Kind: ErrorKindHTTP, Status: 404}).ServerUnavailable() ||
		(&Failure{Kind: ErrorKindInvalid}).ServerUnavailable() {
		t.Fatal("server unavailable classification")
	}
}
