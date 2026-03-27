package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestContextHandler_WithCorrelationID(t *testing.T) {
	var buf bytes.Buffer
	baseHandler := slog.NewJSONHandler(&buf, nil)
	handler := ContextHandler{Handler: baseHandler}
	l := slog.New(handler)

	ctx := context.WithValue(context.Background(), CorrelationIDKey, "test-corr-id-123")
	l.InfoContext(ctx, "test mesaji")

	var logEntry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
		t.Fatalf("Log ciktisi JSON olarak parse edilemedi: %v", err)
	}

	if logEntry["correlation_id"] != "test-corr-id-123" {
		t.Errorf("Beklenen correlation_id 'test-corr-id-123', alinan '%v'", logEntry["correlation_id"])
	}

	if logEntry["msg"] != "test mesaji" {
		t.Errorf("Beklenen msg 'test mesaji', alinan '%v'", logEntry["msg"])
	}
}

func TestContextHandler_WithoutCorrelationID(t *testing.T) {
	var buf bytes.Buffer
	baseHandler := slog.NewJSONHandler(&buf, nil)
	handler := ContextHandler{Handler: baseHandler}
	l := slog.New(handler)

	l.InfoContext(context.Background(), "correlation id olmadan mesaj")

	var logEntry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
		t.Fatalf("Log ciktisi JSON olarak parse edilemedi: %v", err)
	}

	if _, exists := logEntry["correlation_id"]; exists {
		t.Error("Context'te correlation_id yokken log ciktisinda olmamali")
	}
}

func TestContextHandler_EmptyCorrelationID(t *testing.T) {
	var buf bytes.Buffer
	baseHandler := slog.NewJSONHandler(&buf, nil)
	handler := ContextHandler{Handler: baseHandler}
	l := slog.New(handler)

	// Bos string olan correlation ID eklenmemeli
	ctx := context.WithValue(context.Background(), CorrelationIDKey, "")
	l.InfoContext(ctx, "bos correlation id ile mesaj")

	var logEntry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
		t.Fatalf("Log ciktisi JSON olarak parse edilemedi: %v", err)
	}

	if _, exists := logEntry["correlation_id"]; exists {
		t.Error("Bos string correlation_id log ciktisinda olmamali")
	}
}

func TestContextHandler_WrongTypeCorrelationID(t *testing.T) {
	var buf bytes.Buffer
	baseHandler := slog.NewJSONHandler(&buf, nil)
	handler := ContextHandler{Handler: baseHandler}
	l := slog.New(handler)

	// int tipinde value → string type assertion başarısız olacak, eklenmemeli
	ctx := context.WithValue(context.Background(), CorrelationIDKey, 12345)
	l.InfoContext(ctx, "yanlis tipte correlation id")

	var logEntry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
		t.Fatalf("Log ciktisi JSON olarak parse edilemedi: %v", err)
	}

	if _, exists := logEntry["correlation_id"]; exists {
		t.Error("String olmayan correlation_id log ciktisinda olmamali")
	}
}

func TestCorrelationIDKey_Value(t *testing.T) {
	if CorrelationIDKey != "CorrelationID" {
		t.Errorf("Beklenen CorrelationIDKey 'CorrelationID', alinan '%s'", CorrelationIDKey)
	}
}

func TestSetup_DoesNotPanic(t *testing.T) {
	// Setup fonksiyonu panic atmamalı
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Setup panige neden oldu: %v", r)
		}
	}()
	Setup()
}
