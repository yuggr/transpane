// Package translate implements the constrained PLaMo HTTP client.
package translate

import "fmt"

// ErrorKind classifies a translation failure.
type ErrorKind string

const (
	ErrorKindConnect    ErrorKind = "connect"
	ErrorKindTimeout    ErrorKind = "timeout"
	ErrorKindHTTP       ErrorKind = "http"
	ErrorKindInvalid    ErrorKind = "invalid"
	ErrorKindEmpty      ErrorKind = "empty"
	ErrorKindIncomplete ErrorKind = "incomplete"
	ErrorKindCancelled  ErrorKind = "cancelled"
)

func (k ErrorKind) Label() string {
	switch k {
	case ErrorKindConnect:
		return "接続失敗"
	case ErrorKindTimeout:
		return "タイムアウト"
	case ErrorKindHTTP:
		return "HTTPエラー"
	case ErrorKindInvalid:
		return "応答形式不正"
	case ErrorKindEmpty:
		return "空応答"
	case ErrorKindIncomplete:
		return "応答未完了"
	case ErrorKindCancelled:
		return "取消"
	default:
		return string(k)
	}
}

// Failure carries a classified client error.
type Failure struct {
	Kind   ErrorKind
	Status int
}

func (f *Failure) Error() string { return string(f.Kind) }

// ServerUnavailable reports whether the transport or server is unusable.
func (f *Failure) ServerUnavailable() bool {
	if f.Kind == ErrorKindConnect || f.Kind == ErrorKindTimeout {
		return true
	}
	return f.Kind == ErrorKindHTTP && f.Status >= 500
}

func failure(kind ErrorKind, status ...int) *Failure {
	result := &Failure{Kind: kind}
	if len(status) > 0 {
		result.Status = status[0]
	}
	return result
}

func (f *Failure) String() string {
	if f.Status != 0 {
		return fmt.Sprintf("%s (%d)", f.Kind, f.Status)
	}
	return string(f.Kind)
}
