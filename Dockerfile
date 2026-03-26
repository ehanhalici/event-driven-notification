# ==========================================
# 1. BUILD STAGE (Derleme Aşaması)
# ==========================================
FROM golang:alpine AS builder

# Gerekli sistem paketlerini yükle
RUN apk add --no-cache git tzdata

# Çalışma dizinini ayarla
WORKDIR /app

# Önce modül dosyalarını kopyala ve indir (Docker layer cache optimizasyonu için)
COPY go.mod go.sum ./
RUN go mod download

# Tüm kaynak kodunu kopyala
COPY . .

# Binary'leri statik olarak (CGO_ENABLED=0) ve olabilecek en küçük boyutta (-ldflags="-w -s") derle
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /bin/api ./cmd/api/main.go
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /bin/worker ./cmd/worker/main.go
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /bin/relay ./cmd/relay/main.go

# ==========================================
# 2. FINAL STAGE (Çalıştırma Aşaması)
# ==========================================
# İşletim sistemi dahi olmayan (veya en hafifi olan) Alpine kullanıyoruz.
# Böylece kaynak kodların veya Go derleyicisi canlı ortama gitmez, güvenlik artar.
FROM alpine:latest

# SSL sertifikaları (dış Webhook'lara istek atabilmek için) ve saat dilimi
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

# Sadece derlenmiş çalıştırılabilir (executable) dosyaları builder aşamasından al
COPY --from=builder /bin/api /app/api
COPY --from=builder /bin/worker /app/worker
COPY --from=builder /bin/relay /app/relay

# Portları belgele (API: 8080, Worker Metrics: 9091, Relay Metrics: 9094)
EXPOSE 8080 9091 9094

# Varsayılan olarak API'yi çalıştır 
# (docker-compose içinde command parametresi ile bu ezilerek worker veya relay çalıştırılır)
CMD ["/app/api"]
