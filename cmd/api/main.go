package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"insider-notification/internal/api"
	"insider-notification/internal/config"
	"insider-notification/internal/logger"
	"insider-notification/internal/ratelimit"
	"insider-notification/internal/repository"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

func main() {
	logger.Setup()
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		slog.Error("Konfigurasyon eksik veya hatali (Uygulama baslatilamiyor)", "error", err)
		os.Exit(1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Veritabanı bağlantısı
	dbPool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("Veritabanina baglanilamadi", "error", err)
		os.Exit(1)
	}
	defer dbPool.Close()

	repo := &repository.DB{Pool: dbPool}

	rdb := redis.NewClient(&redis.Options{
		Addr: cfg.RedisURL,
	})
	defer rdb.Close()

	// Router (Mux) ayarları
	mux := api.SetupRoutes(repo, rdb)
	limiter := ratelimit.NewLimiter(rdb)

	// Middleware Zinciri (Soğan Zarı Modeli - Onion Architecture)
	// İstek önce Rate Limiter'a çarpar, geçerse Request ID atanır, geçerse Mux'a (Router) ulaşır.
	handler := api.RateLimitMiddleware(limiter)(mux)
	handler = api.RequestIDMiddleware(handler)

	srv := &http.Server{
		Addr:    cfg.APIPort,
		Handler: handler, // Zırhlanmış handler'ı veriyoruz
	}
	
	// Sunucuyu arka planda (goroutine) başlat
	go func() {
		slog.Info("API Server baslatiliyor", "port", cfg.APIPort)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("API sunucusu beklenmedik sekilde coktu", "error", err)
			os.Exit(1)
		}
	}()

	// İşletim sistemi sinyallerini dinle
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	slog.Info("Kapanis sinyali alindi, API sunucusu zarifce durduruluyor...")

	// Yeni gelen istekleri reddet, mevcut isteklerin bitmesi için 10 saniye süre tanı
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("Sunucu kapatilirken hata olustu", "error", err)
	}

	slog.Info("API Sunucusu guvenle kapatildi.")
	
}
