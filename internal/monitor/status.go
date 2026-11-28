package monitor

// ConnectionState is the last known transport state.
type ConnectionState string

const (
	ConnectionUnknown   ConnectionState = "unknown"
	ConnectionConnected ConnectionState = "connected"
	ConnectionFailed    ConnectionState = "failed"
)

func (c ConnectionState) Label() string {
	switch c {
	case ConnectionConnected:
		return "接続成功"
	case ConnectionFailed:
		return "通信失敗"
	default:
		return "未確認"
	}
}

// Status is the immutable status snapshot shown by the TUI.
type Status struct {
	Server     string
	Pending    int
	LastUpdate string
	Note       string
	Error      string
	CacheNote  string
	Session    string
}
