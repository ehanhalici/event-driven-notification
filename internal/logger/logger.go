// internal/logger/logger.go
package logger

import (
	"context"
	"log/slog"
	"os"
)

// Go best-practice: Context anahtarları string olmamalıdır, özel type olmalıdır.
type ContextKey string

const CorrelationIDKey ContextKey = "CorrelationID"

// ContextHandler, standart logları sarmalayıp araya giren özel bir katmandır
type ContextHandler struct {
	slog.Handler
}

// Handle fonksiyonu her slog.Info veya slog.Error çağrıldığında araya girer
func (h ContextHandler) Handle(ctx context.Context, r slog.Record) error {
	// Context'ten CorrelationID'yi çek
	if corrID, ok := ctx.Value(CorrelationIDKey).(string); ok && corrID != "" {
		// Log satırına otomatik olarak ekle
		r.AddAttrs(slog.String("correlation_id", corrID))
	}
	return h.Handler.Handle(ctx, r)
}

// Setup, uygulamanın başında çağrılacak
func Setup() {
	jsonHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})

	// JSON handler'ı kendi ContextHandler'ımız ile sarmalıyoruz
	logger := slog.New(ContextHandler{Handler: jsonHandler})
	slog.SetDefault(logger)
}
