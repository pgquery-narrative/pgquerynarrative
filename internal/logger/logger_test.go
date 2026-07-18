package logger

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestNewFromConfig_invalidLevel_fallsBackToInfo(t *testing.T) {
	l := NewFromConfig("invalid", false)
	if l.z == nil {
		t.Fatal("NewFromConfig should produce zerolog backend")
	}
	if got := l.z.GetLevel(); got != zerolog.InfoLevel {
		t.Errorf("expected fallback level %v for invalid level, got %v", zerolog.InfoLevel, got)
	}
}

func TestNewFromConfig_emptyLevel_defaultsToInfo(t *testing.T) {
	l := NewFromConfig("", false)
	if l.z == nil {
		t.Fatal("NewFromConfig should produce zerolog backend for empty level")
	}
	if got := l.z.GetLevel(); got != zerolog.InfoLevel {
		t.Errorf("expected default level %v for empty level, got %v", zerolog.InfoLevel, got)
	}
}

func TestNewFromConfig_zerologLogsMessageAndFields(t *testing.T) {
	buf := new(bytes.Buffer)
	l := newFromConfigOutput("info", false, buf)
	l.Info("http request", "method", "GET", "path", "/health", "http.status_code", 200)
	out := buf.String()
	if !strings.Contains(out, "http request") {
		t.Errorf("log should contain message: %s", out)
	}
	if !strings.Contains(out, "GET") || !strings.Contains(out, "/health") || !strings.Contains(out, "200") {
		t.Errorf("log should contain fields: %s", out)
	}
}

func TestNewFromConfig_zerologErrLevel(t *testing.T) {
	buf := new(bytes.Buffer)
	l := newFromConfigOutput("info", false, buf)
	l.Err("error response", "http.status_code", 500, "path", "/api/v1/run")
	out := buf.String()
	if !strings.Contains(out, "error response") || !strings.Contains(out, "500") {
		t.Errorf("Err should log message and fields: %s", out)
	}
}

func TestNewFromConfig_zerologLogsErrorField(t *testing.T) {
	buf := new(bytes.Buffer)
	l := newFromConfigOutput("info", false, buf)
	err := errors.New("boom")
	l.Info("failed to do something", "error", err)
	out := buf.String()
	if !strings.Contains(out, "error") {
		t.Errorf("log should contain error key: %s", out)
	}
	if !strings.Contains(out, "boom") {
		t.Errorf("log should contain error value: %s", out)
	}
}

func TestNewFromConfig_zerologOddKeyValueArgsAreIgnored(t *testing.T) {
	buf := new(bytes.Buffer)
	l := newFromConfigOutput("info", false, buf)
	l.Info("with fields", "key1", "value1", "dangling")
	out := buf.String()
	if !strings.Contains(out, "key1") || !strings.Contains(out, "value1") {
		t.Errorf("log should contain complete key/value pair: %s", out)
	}
	if strings.Contains(out, "dangling") {
		t.Errorf("log should ignore dangling key without value: %s", out)
	}
}

func TestLogger_Debug_noOpWhenWriterBacked(t *testing.T) {
	buf := new(bytes.Buffer)
	l := New(buf)
	l.Debug("should not appear", "x", "y")
	if buf.Len() != 0 {
		t.Errorf("Debug with writer-backed logger should be no-op, got %q", buf.String())
	}
}

func TestLogger_Debug_emitsWhenZerologBacked(t *testing.T) {
	buf := new(bytes.Buffer)
	l := newFromConfigOutput("debug", false, buf)
	l.Debug("debug message", "key", "val")
	out := buf.String()
	if !strings.Contains(out, "debug message") || !strings.Contains(out, "val") {
		t.Errorf("Debug with zerolog should emit: %s", out)
	}
}

func TestLogger_Info_Err_withWriterBacked(t *testing.T) {
	buf := new(bytes.Buffer)
	l := New(buf)
	l.Info("hello", "component", "test")
	l.Err("oops", "error", "something failed")
	out := buf.String()
	if !strings.Contains(out, "INF") || !strings.Contains(out, "hello") {
		t.Errorf("Info should write INF and message: %s", out)
	}
	if !strings.Contains(out, "ERR") || !strings.Contains(out, "oops") {
		t.Errorf("Err should write ERR and message: %s", out)
	}
}
