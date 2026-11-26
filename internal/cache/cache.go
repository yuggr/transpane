// Package cache stores marker-validated translations in the existing SQLite
// database. Failures disable the cache for the process and never modify an
// existing file.
package cache

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

const createTable = `CREATE TABLE IF NOT EXISTS translations (
    cache_key TEXT PRIMARY KEY,
    translated TEXT NOT NULL,
    created_at TEXT NOT NULL
)`

// Status describes whether the cache can be used.
type Status struct {
	Available bool
	Path      string
	Message   string
}

// Cache is a single-connection, non-repairing translation cache.
type Cache struct {
	path        string
	available   bool
	message     string
	db          *sql.DB
	initialized bool
}

// Open initializes the cache. A failure is reported through Status and the
// instance stays disabled for its lifetime.
func Open(path string) *Cache {
	cache := &Cache{path: path, available: true}
	cache.initialize()
	return cache
}

func (c *Cache) Status() Status {
	message := ""
	if !c.available {
		message = c.message
	}
	return Status{Available: c.available, Path: c.path, Message: message}
}

func (c *Cache) Get(key string) (string, bool) {
	if !c.available || c.db == nil {
		return "", false
	}
	var translated string
	err := c.db.QueryRow("SELECT translated FROM translations WHERE cache_key = ?", key).Scan(&translated)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", false
		}
		c.disable(classify(err))
		return "", false
	}
	return translated, true
}

func (c *Cache) Put(key, translated, createdAt string) {
	if !c.available || c.db == nil {
		return
	}
	if _, err := c.db.Exec("INSERT OR REPLACE INTO translations (cache_key, translated, created_at) VALUES (?, ?, ?)", key, translated, createdAt); err != nil {
		c.disable(classify(err))
	}
}

func (c *Cache) Close() error {
	if c.db == nil {
		return nil
	}
	err := c.db.Close()
	c.db = nil
	return err
}

func (c *Cache) initialize() {
	if c.initialized {
		return
	}
	c.initialized = true
	directory := filepath.Dir(c.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		c.disable("キャッシュを利用できません")
		return
	}
	dirInfo, err := os.Stat(directory)
	if err != nil || !dirInfo.IsDir() {
		c.disable("キャッシュを利用できません")
		return
	}
	if dirInfo.Mode().Perm()&0o077 != 0 {
		c.disable("キャッシュディレクトリの権限が広すぎます")
		return
	}

	info, err := os.Lstat(c.path)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			c.disable("キャッシュDBの権限が広すぎます")
			return
		}
		if info.Mode().Perm()&0o077 != 0 {
			c.disable("キャッシュDBの権限が広すぎます")
			return
		}
	case os.IsNotExist(err):
		file, createErr := os.OpenFile(c.path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		if createErr != nil {
			c.disable("キャッシュを利用できません")
			return
		}
		_ = file.Close()
		if chmodErr := os.Chmod(c.path, 0o600); chmodErr != nil {
			c.disable("キャッシュを利用できません")
			return
		}
	default:
		c.disable("キャッシュを利用できません")
		return
	}

	db, err := sql.Open("sqlite", "file:"+c.path+"?_pragma=busy_timeout(500)")
	if err != nil {
		c.disable("キャッシュを利用できません")
		return
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(createTable); err != nil {
		_ = db.Close()
		c.disable(classify(err))
		return
	}
	c.db = db
}

func (c *Cache) disable(message string) {
	if c.available {
		c.available = false
		c.message = message
	}
}

func classify(err error) string {
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "locked") || strings.Contains(text, "busy"):
		return "キャッシュDBがロックされています"
	case strings.Contains(text, "not a database") || strings.Contains(text, "corrupt") ||
		strings.Contains(text, "malformed") || strings.Contains(text, "disk i/o"):
		return "キャッシュDBを読み書きできません（破損の可能性）"
	default:
		return "キャッシュを利用できません"
	}
}
