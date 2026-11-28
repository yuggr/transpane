// Package config loads the strict TOML configuration and validates the
// loopback-only translation endpoint.
package config

import (
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

const (
	DefaultBaseURL = "http://127.0.0.1:1234/v1"
	DefaultForm    = "polite"

	SourceLang = "en"
	TargetLang = "ja"

	CacheSchemaVersion       = 1
	TranslationPolicyVersion = 1

	PollIntervalSeconds        = 0.25
	SessionListIntervalSeconds = 2.0
	ConnectTimeoutSeconds      = 3.0
	ReadTimeoutSeconds         = 60.0
	TotalTimeoutSeconds        = 90.0
	MaxSegmentChars            = 1000
	InitialMessageCount        = 50
	HistoryPageSize            = 50
	MaxPendingJobs             = 32
	MaxLineBytes               = 8 * 1024 * 1024
	MaxPollBytes               = 1024 * 1024
	InitialScanChunkBytes      = 1024 * 1024
	MetaReadLimit              = 1024 * 1024
)

var forms = map[string]bool{"polite": true, "plain": true}

var rfc1918 = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
}

// Config is the resolved runtime configuration.
type Config struct {
	CodexHome          string
	SessionsRoot       string
	ClaudeHome         string
	ClaudeProjectsRoot string
	BaseURL            string
	Model              string
	Form               string
	CachePath          string
	ConfigPath         string // empty when no config file exists
}

// ConfigError reports an invalid or missing configuration value.
type ConfigError struct{ Message string }

func (e *ConfigError) Error() string { return e.Message }

func errorf(format string, args ...any) error {
	return &ConfigError{Message: fmt.Sprintf(format, args...)}
}

// Load resolves configuration from the process environment.
func Load() (Config, error) {
	env := make(map[string]string)
	for _, item := range os.Environ() {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			env[key] = value
		}
	}
	home, _ := os.UserHomeDir()
	return LoadFrom(env, home)
}

// LoadFrom resolves configuration from an explicit environment and home
// directory. It never reads a home directory from the process when one is
// supplied, which keeps tests hermetic.
func LoadFrom(env map[string]string, home string) (Config, error) {
	if home == "" {
		home = env["HOME"]
	}
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	codexHome := expandPath(valueOr(env, "CODEX_HOME", "~/.codex"), home)
	claudeHome := expandPath(valueOr(env, "CLAUDE_CONFIG_DIR", "~/.claude"), home)
	configHome := expandPath(valueOr(env, "XDG_CONFIG_HOME", "~/.config"), home)
	cacheHome := expandPath(valueOr(env, "XDG_CACHE_HOME", "~/.cache"), home)

	configPath := filepath.Join(configHome, "transpane", "config.toml")
	data := map[string]any{}
	existing := ""
	if _, err := os.Stat(configPath); err == nil {
		raw, err := os.ReadFile(configPath) //nolint:gosec // the config path is user-selected by design
		if err != nil {
			return Config{}, errorf("設定ファイルを読み込めません: %s", configPath)
		}
		if err := toml.Unmarshal(raw, &data); err != nil {
			return Config{}, errorf("設定ファイルを読み込めません: %s", configPath)
		}
		existing = configPath
	}

	unknownTop := make([]string, 0)
	for key := range data {
		if key != "translation" {
			unknownTop = append(unknownTop, key)
		}
	}
	if len(unknownTop) > 0 {
		sort.Strings(unknownTop)
		return Config{}, errorf("不明な設定項目です: %s", strings.Join(unknownTop, ", "))
	}

	translation := map[string]any{}
	if raw, ok := data["translation"]; ok {
		var typeOK bool
		translation, typeOK = raw.(map[string]any)
		if !typeOK {
			return Config{}, errorf("[translation] はテーブルである必要があります")
		}
	}

	unknown := make([]string, 0)
	for key := range translation {
		if key != "base_url" && key != "model" && key != "form" {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return Config{}, errorf("不明な設定項目です: %s", strings.Join(unknown, ", "))
	}
	for _, key := range []string{"base_url", "model", "form"} {
		if value, ok := translation[key]; ok {
			if _, isString := value.(string); !isString {
				return Config{}, errorf("translation.%s は文字列である必要があります", key)
			}
		}
	}

	model := strings.TrimSpace(stringValue(translation, "model"))
	if model == "" {
		return Config{}, errorf("translation.model は必須です（/v1/models の実IDを設定してください）")
	}
	form := DefaultForm
	if value, present := translation["form"]; present {
		form = value.(string)
	}
	if !forms[form] {
		return Config{}, errorf("translation.form は polite または plain を指定してください")
	}
	base := DefaultBaseURL
	if value, present := translation["base_url"]; present {
		base = value.(string)
	}
	baseURL, err := ValidateBaseURL(base)
	if err != nil {
		return Config{}, err
	}

	return Config{
		CodexHome:          codexHome,
		SessionsRoot:       filepath.Join(codexHome, "sessions"),
		ClaudeHome:         claudeHome,
		ClaudeProjectsRoot: filepath.Join(claudeHome, "projects"),
		BaseURL:            baseURL,
		Model:              model,
		Form:               form,
		CachePath:          filepath.Join(cacheHome, "transpane", "translations.sqlite3"),
		ConfigPath:         existing,
	}, nil
}

func valueOr(env map[string]string, key, fallback string) string {
	if value := env[key]; value != "" {
		return value
	}
	return fallback
}

func stringValue(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func expandPath(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}

// ValidateBaseURL accepts only http, an IP literal, loopback or RFC1918, and
// the /v1 path. Names, userinfo, query, fragment, public addresses, zones and
// IPv4-mapped IPv6 are rejected.
func ValidateBaseURL(raw string) (string, error) {
	if raw == "" {
		return "", errorf("translation.base_url が空です")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", errorf("translation.base_url を解析できません")
	}
	if parsed.Scheme != "http" {
		return "", errorf("translation.base_url は http:// で始まる必要があります")
	}
	if parsed.User != nil {
		return "", errorf("translation.base_url に userinfo を含められません")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", errorf("translation.base_url に query/fragment を含められません")
	}
	path := strings.TrimRight(parsed.EscapedPath(), "/")
	if path == "" {
		path = "/"
	}
	if path != "/v1" {
		return "", errorf("translation.base_url のパスは /v1 に限定されます")
	}

	host := parsed.Hostname()
	if host == "" {
		return "", errorf("translation.base_url にホストがありません")
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return "", errorf("translation.base_url は IP リテラルである必要があります")
	}
	if address.Zone() != "" {
		return "", errorf("translation.base_url に zone を含められません")
	}
	if !localOrPrivate(address) {
		return "", errorf("translation.base_url は loopback または RFC1918 のみ許可されます")
	}

	port := parsed.Port()
	if port == "" {
		port = "80"
	} else {
		number, convErr := strconv.Atoi(port)
		if convErr != nil || number <= 0 || number > 65535 {
			return "", errorf("translation.base_url のポートが不正です")
		}
	}
	hostPart := address.String()
	if address.Is6() {
		hostPart = "[" + hostPart + "]"
	}
	return fmt.Sprintf("http://%s:%s/v1", hostPart, port), nil
}

func localOrPrivate(address netip.Addr) bool {
	if address.Is4In6() || !address.IsValid() {
		return false
	}
	if address.IsLoopback() {
		return true
	}
	if !address.Is4() {
		return false
	}
	for _, prefix := range rfc1918 {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
