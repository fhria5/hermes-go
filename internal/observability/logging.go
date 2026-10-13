package observability

import (
	"log/slog"
	"os"
	"strings"
)

func NewLogger() *slog.Logger {
	lvl := slog.LevelInfo
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(h)
}

// Sanitize membuang kemungkinan secret dari string sebelum masuk log.
func Sanitize(s string) string {
	for _, pat := range []string{"Bearer ", "sk-", "xoxb-", "bot"} {
		if i := strings.Index(s, pat); i >= 0 {
			if j := strings.IndexAny(s[i:], " \"'}\n"); j > 0 {
				s = s[:i] + "[REDACTED]" + s[i+j:]
			} else {
				s = s[:i] + "[REDACTED]"
			}
		}
	}
	return s
}
