// Package debuglog provides optional verbose logging when LOG_DEBUG is enabled.
// Messages use the same structured format (timestamp, level, key=value) as the rest of the app.
package debuglog

import (
	"fmt"
	"os"
	"strings"

	"github.com/pgquerynarrative/pgquerynarrative/internal/logger"
)

func enabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_DEBUG")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// Log writes a log message when LOG_DEBUG is set (1, true, yes, on).
func Log(format string, args ...interface{}) {
	if enabled() {
		logger.DefaultLogger().Info("debug", "component", "debug", "msg", fmt.Sprintf(format, args...))
	}
}
