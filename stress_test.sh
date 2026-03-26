#!/bin/bash

API_URL="http://localhost:8080"
CHANNELS=("sms" "email" "push")
PRIORITIES=("low" "normal" "high")

# Yanıtı parse edebiliyorsa jq ile gösteren, edemiyorsa ham halini basan fonksiyon
safe_jq() {
    local input
    input=$(cat)
    if echo "$input" | jq . >/dev/null 2>&1; then
        echo "$input" | jq .
    else
        echo -e "\e[33m[Ham Yanıt]:\e[0m $input"
    fi
}

# Rastgele ve geçerli JSON verisi üretme fonksiyonu (jq kullanarak tırnak hatalarını önler)
generate_data() {
    local channel=${CHANNELS[$RANDOM % ${#CHANNELS[@]}]}
    local priority=${PRIORITIES[$RANDOM % ${#PRIORITIES[@]}]}
    local idemp=$(cat /proc/sys/kernel/random/uuid)
    local recipient="+90555$(printf "%07d" $((RANDOM % 10000000)))"
    
    if [ "$channel" == "email" ]; then
        recipient="user_$RANDOM@example.com"
    fi

    jq -n \
      --arg id "$idemp" \
      --arg rec "$recipient" \
      --arg chan "$channel" \
      --arg prio "$priority" \
      --arg cont "Mesaj içeriği - $RANDOM" \
      '{idempotency_key: $id, recipient: $rec, channel: $chan, content: $cont, priority: $prio}'
}

echo -e "\e[36m Insider Notification System Yük Testi Başlatılıyor...\e[0m"

# 1. Tekil İstek Gönderimi
echo "------------------------------------------"
echo "1. Tekil Bildirimler Gönderiliyor (/notifications)..."
for i in {1..5}
do
    DATA=$(generate_data)
    echo -n "İstek $i: "
    curl -s -X POST "$API_URL/notifications" \
         -H "Content-Type: application/json" \
         -d "$DATA" | safe_jq
    sleep 0.3
done

# 2. Toplu İstek Gönderimi
echo "------------------------------------------"
echo "2. Toplu Bildirim Paketi Gönderiliyor (/notifications/batch)..."

# 5 adet rastgele objeyi jq ile temiz bir array'e dönüştürür
BATCH_DATA=$(for i in {1..5}; do generate_data; done | jq -s .)

curl -s -X POST "$API_URL/notifications/batch" \
     -H "Content-Type: application/json" \
     -d "$BATCH_DATA" | safe_jq

echo "------------------------------------------"
echo -e "\e[32m İşlem tamamlandı. Logları kontrol edebilirsin.\e[0m"
