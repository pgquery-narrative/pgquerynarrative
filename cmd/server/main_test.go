package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/logger"
)

func TestRequestLoggingMiddleware_errorResponseLogsBody(t *testing.T) {
	logBuf := new(bytes.Buffer)
	appLogger := logger.New(logBuf)

	handler := requestLoggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"name":"validation_error","message":"only SELECT allowed"}`))
	}), appLogger, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/queries/run", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	logOut := logBuf.String()
	if !strings.Contains(logOut, "POST") || !strings.Contains(logOut, "/api/v1/queries/run") || !strings.Contains(logOut, "400") {
		t.Errorf("log should contain method, path, status: %s", logOut)
	}
	if !strings.Contains(logOut, "error response") {
		t.Errorf("log should contain error response line: %s", logOut)
	}
	if !strings.Contains(logOut, "only SELECT allowed") {
		t.Errorf("log should contain response body (error message): %s", logOut)
	}
}

func TestRequestLoggingMiddleware_successDoesNotLogErrorLine(t *testing.T) {
	logBuf := new(bytes.Buffer)
	appLogger := logger.New(logBuf)

	handler := requestLoggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"columns":[]}`))
	}), appLogger, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/queries/run", nil)
	req.RemoteAddr = "127.0.0.1:8080"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	logOut := logBuf.String()
	if strings.Contains(logOut, "error response") {
		t.Errorf("success response should not log error response line: %s", logOut)
	}
}
