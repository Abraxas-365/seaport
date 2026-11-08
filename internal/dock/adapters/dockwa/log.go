package dockwa

import (
	"fmt"
	"log/slog"

	waLog "go.mau.fi/whatsmeow/util/log"
)

// slogLogger adapts whatsmeow's logger to slog. whatsmeow is chatty at info
// level, so its info goes to debug.
type slogLogger struct{ l *slog.Logger }

func newLogger(module string) waLog.Logger {
	return slogLogger{l: slog.Default().With("component", "whatsmeow", "module", module)}
}

func (s slogLogger) Warnf(msg string, args ...any)  { s.l.Warn(fmt.Sprintf(msg, args...)) }
func (s slogLogger) Errorf(msg string, args ...any) { s.l.Error(fmt.Sprintf(msg, args...)) }
func (s slogLogger) Infof(msg string, args ...any)  { s.l.Debug(fmt.Sprintf(msg, args...)) }
func (s slogLogger) Debugf(msg string, args ...any) {}
func (s slogLogger) Sub(module string) waLog.Logger {
	return slogLogger{l: s.l.With("sub", module)}
}
