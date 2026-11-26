package cache

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestRoundtripAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "translations.sqlite3")
	cache := Open(path)
	if status := cache.Status(); !status.Available {
		t.Fatalf("unavailable: %s", status.Message)
	}
	dirInfo, _ := os.Stat(filepath.Dir(path))
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %o", dirInfo.Mode().Perm())
	}
	fileInfo, _ := os.Stat(path)
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %o", fileInfo.Mode().Perm())
	}
	cache.Put("key", "訳 @@CJ0@@", "2026-11-25T00:00:00+00:00")
	if got, ok := cache.Get("key"); !ok || got != "訳 @@CJ0@@" {
		t.Fatalf("%q %v", got, ok)
	}
	if _, ok := cache.Get("missing"); ok {
		t.Fatal("missing key found")
	}
	cache.Close()

	reopened := Open(path)
	if !reopened.Status().Available {
		t.Fatal("reopen failed")
	}
	if got, ok := reopened.Get("key"); !ok || got != "訳 @@CJ0@@" {
		t.Fatal("reopen lost value")
	}
	reopened.Close()
}

func TestWideDirectoryDisabledWithoutChmod(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "wide")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(directory, 0o755)
	cache := Open(filepath.Join(directory, "translations.sqlite3"))
	status := cache.Status()
	if status.Available || !contains(status.Message, "権限") {
		t.Fatalf("%+v", status)
	}
	info, _ := os.Stat(directory)
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("chmod happened: %o", info.Mode().Perm())
	}
	cache.Put("x", "y", "now")
	if _, ok := cache.Get("x"); ok {
		t.Fatal("disabled cache returned value")
	}
}

func TestWideFileDisabled(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "cache")
	_ = os.Mkdir(directory, 0o700)
	path := filepath.Join(directory, "db.sqlite3")
	_ = os.WriteFile(path, nil, 0o644)
	_ = os.Chmod(path, 0o644)
	cache := Open(path)
	if cache.Status().Available {
		t.Fatal("wide file accepted")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o644 {
		t.Fatal("file chmod happened")
	}
}

func TestCorruptDatabasePreserved(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "cache")
	_ = os.Mkdir(directory, 0o700)
	path := filepath.Join(directory, "db.sqlite3")
	content := []byte("this is not sqlite")
	_ = os.WriteFile(path, content, 0o600)
	_ = os.Chmod(path, 0o600)
	cache := Open(path)
	if cache.Status().Available {
		t.Fatal("corrupt database accepted")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(content) {
		t.Fatal("corrupt file modified")
	}
}

func TestLockedDatabaseDisabled(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "cache")
	_ = os.Mkdir(directory, 0o700)
	path := filepath.Join(directory, "db.sqlite3")
	cache := Open(path)
	if !cache.Status().Available {
		t.Fatal("initial open failed")
	}
	defer cache.Close()

	locker, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(100)")
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	locker.SetMaxOpenConns(1)
	if _, err := locker.Exec("BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	defer locker.Exec("ROLLBACK")
	if _, ok := cache.Get("x"); ok {
		t.Fatal("locked get returned value")
	}
	if cache.Status().Available {
		t.Fatal("locked cache still available")
	}
}

func TestUnusualKeysAndRawReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c", "db.sqlite3")
	cache := Open(path)
	for _, key := range []string{"", "aaaaaaaaaa", "日本語"} {
		cache.Put(key, "value-"+key, "now")
		if got, ok := cache.Get(key); !ok || got != "value-"+key {
			t.Fatalf("%q -> %q %v", key, got, ok)
		}
	}
	cache.Close()

	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var translated string
	if err := raw.QueryRow("SELECT translated FROM translations WHERE cache_key = ?", "日本語").Scan(&translated); err != nil || translated != "value-日本語" {
		t.Fatalf("%q %v", translated, err)
	}
}

func contains(value, needle string) bool {
	return len(value) >= len(needle) && (value == needle || len(needle) == 0 ||
		(len(value) > 0 && (indexOf(value, needle) >= 0)))
}

func indexOf(value, needle string) int {
	for i := 0; i+len(needle) <= len(value); i++ {
		if value[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
