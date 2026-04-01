---------------- MODULE NotificationSystem ----------------
EXTENDS Integers, Sequences, FiniteSets, TLC

(* --- SİSTEM SABİTLERİ VE KÜMELERİ --- *)
CONSTANTS 
    MessageIDs,    (* Sistemdeki tüm olası mesaj ID'leri kümesi (örn: {"m1", "m2"}) *)
    MaxRetry       (* Bir mesajın en fazla kaç kez deneneceği (örn: 3) *)

(* Bildirimlerin alabileceği tüm veritabanı durumları *)
States == {"pending", "processing", "delivered", "failed_retrying", "failed_permanently"}

(* --- SİSTEM DEĞİŞKENLERİ --- *)
VARIABLES 
    db_notifications, (* notifications tablosu: ID -> [state, retry_count] *)
    db_outbox,        (* outbox_events tablosu: Bekleyen mesaj ID'leri kümesi *)
    kafka_topic,      (* Kafka kuyruğu: Sequence (dizi) olarak modellenir *)
    dlq_topic         (* Dead Letter Queue: Kalıcı hatalı mesajlar *)

vars == <<db_notifications, db_outbox, kafka_topic, dlq_topic>>

(* --- YARDIMCI OPERATÖRLER --- *)
(* Sistemin herhangi bir anında geçerli (Type-Safe) durumda olduğunu doğrulayan Invariant *)
TypeOK == 
    /\ DOMAIN db_notifications \subseteq MessageIDs
    /\ \A m \in DOMAIN db_notifications : 
        /\ db_notifications[m].state \in States
        /\ db_notifications[m].retry_count \in 0..MaxRetry
    /\ db_outbox \subseteq MessageIDs
    /\ kafka_topic \in Seq(MessageIDs)
    /\ dlq_topic \in Seq(MessageIDs)

Init == 
    /\ db_notifications = [m \in {} |-> ""] (* Boş fonksiyon (tablo) *)
    /\ db_outbox = {}                      (* Boş küme *)
    /\ kafka_topic = <<>>                  (* Boş dizi *)
    /\ dlq_topic = <<>>


ApiCreateNotification(m) ==
    (* Ön koşul: Bu mesaj ID'si daha önce sisteme girmemiş olmalı *)
    (* [IMPLEMENTATION NOTE]: TLA+'taki bu proaktif (proactive) guard, *)
    (* gerçek dünyada Race Condition (TOCTOU) yaratmamak adına Go tarafında *)
    (* reaktif (reactive) olarak Postgres UNIQUE CONSTRAINT ve *)
    (* 'pgErr.Code == "23505"' kontrolü ile implemente edilmiştir. *)
    /\ m \notin DOMAIN db_notifications
    
    (* Etki: db_notifications tablosuna 'pending' statüsünde ekle *)
    /\ db_notifications' = db_notifications @@ (m :> [state |-> "pending", retry_count |-> 0])
    
    (* Etki: db_outbox tablosuna ekle *)
    /\ db_outbox' = db_outbox \cup {m}
    
    (* Diğer sistem değişkenleri sabit kalır *)
    /\ UNCHANGED <<kafka_topic, dlq_topic>>

RelayProcessOutbox ==
    (* Ön koşul: Outbox tablosunda işlenecek en az bir mesaj olmalı *)
    /\ db_outbox /= {}
    
    (* Etki: Outbox'taki mesajlardan birini (veya batch olarak hepsini) seç *)
    /\ \E m \in db_outbox :
        (* Kafka kuyruğunun sonuna (Append) ekle *)
        /\ kafka_topic' = Append(kafka_topic, m)
        (* Outbox tablosundan sil *)
        /\ db_outbox' = db_outbox \ {m}
        (* Bildirim tablosunun durumu değişmez *)
        /\ UNCHANGED <<db_notifications, dlq_topic>>

WorkerFetchAndLock ==
    (* Ön koşul: Kafka kuyruğunda okunacak mesaj olmalı *)
    /\ kafka_topic /= <<>>
    /\ LET m == Head(kafka_topic) IN
        (* Eğer mesaj DB'de işlenmeye müsaitse (pending veya failed_retrying) *)
        /\ db_notifications[m].state \in {"pending", "failed_retrying"}
        (* Etki: Veritabanında durumu 'processing' yap (LockNotificationForProcessing) *)
        /\ db_notifications' = [db_notifications EXCEPT ![m].state = "processing"]
        (* DİKKAT: Kafka'dan henüz SİLMEDİK (Commit atmadık)! *)
        /\ UNCHANGED <<db_outbox, kafka_topic, dlq_topic>>

WorkerIdempotencySkip ==
    /\ kafka_topic /= <<>>
    /\ LET m == Head(kafka_topic) IN
        (* Mesaj DB'de alınmaya uygun DEĞİLSE (başka worker almışsa veya bitmişse) *)
        /\ db_notifications[m].state \notin {"pending", "failed_retrying"}
        (* Etki: DB'ye dokunma, sadece Kafka'dan mesajı sil (Commit) *)
        /\ kafka_topic' = Tail(kafka_topic)
        /\ UNCHANGED <<db_notifications, db_outbox, dlq_topic>>

WorkerProcessSuccess ==
    /\ kafka_topic /= <<>>
    /\ LET m == Head(kafka_topic) IN
        (* Worker mesajı başarıyla kilitlemiş olmalı *)
        /\ db_notifications[m].state = "processing"
        (* Etki: Durumu 'delivered' yap *)
        /\ db_notifications' = [db_notifications EXCEPT ![m].state = "delivered"]
        (* Etki: Kafka'dan sil (CommitMessages) *)
        /\ kafka_topic' = Tail(kafka_topic)
        /\ UNCHANGED <<db_outbox, dlq_topic>>

WorkerProcessFail ==
    /\ kafka_topic /= <<>>
    /\ LET m == Head(kafka_topic) IN
        /\ db_notifications[m].state = "processing"
        /\ IF db_notifications[m].retry_count < MaxRetry
           THEN (* Tekrar denenecek *)
                /\ db_notifications' = [db_notifications EXCEPT 
                                        ![m].state = "failed_retrying",
                                        ![m].retry_count = @ + 1]
                /\ kafka_topic' = Tail(kafka_topic)
                /\ UNCHANGED <<dlq_topic>>
           ELSE (* Kalıcı başarısız, DLQ'ya at *)
                /\ db_notifications' = [db_notifications EXCEPT ![m].state = "failed_permanently"]
                /\ dlq_topic' = Append(dlq_topic, m)
                /\ kafka_topic' = Tail(kafka_topic)
        /\ UNCHANGED <<db_outbox>>

(* Go'daki HTTP 4xx (Bad Request vb.) kalıcı hata durumunu (maxRetries=0) modeller. *)
(* Retry sayısına bakılmaksızın doğrudan failed_permanently durumuna geçer ve DLQ'ya yazar. *)
WorkerProcessPermanentFail ==
    /\ kafka_topic /= <<>>
    /\ LET m == Head(kafka_topic) IN
        /\ db_notifications[m].state = "processing"
        
        (* DB'de durumu kalıcı hata yap *)
        /\ db_notifications' = [db_notifications EXCEPT ![m].state = "failed_permanently"]
        
        (* Mesajı doğrudan Dead Letter Queue'ya (DLQ) at *)
        /\ dlq_topic' = Append(dlq_topic, m)
        
        (* Kafka'dan offset'i sil (Commit) *)
        /\ kafka_topic' = Tail(kafka_topic)
        
        (* Outbox tablosuyla işimiz yok *)
        /\ UNCHANGED <<db_outbox>>

WorkerRateLimited ==
    /\ kafka_topic /= <<>>
    /\ LET m == Head(kafka_topic) IN
        (* Mesaj Kafka'dan alındı ama API Rate Limit'e takıldı (429) *)
        /\ db_notifications[m].state = "processing"
        
        (* Etki: Durumu tekrar 'failed_retrying' (veya yeni bir 'rate_limited' statüsü) yap. *)
        (* DİKKAT: retry_count ARTIRILMIYOR! Çünkü bu kalıcı veya zehirli bir hata değil, geçici bir yoğunluk. *)
        /\ db_notifications' = [db_notifications EXCEPT ![m].state = "failed_retrying"]
        
        (* Etki: Kafka'dan sil (çünkü Outbox/Relay onu süresi gelince tekrar kuyruğa atacak) *)
        /\ kafka_topic' = Tail(kafka_topic)
        /\ UNCHANGED <<db_outbox, dlq_topic>>

(* msg1 mesajı WorkerIdempotencySkip, ZombieSweeper, Relay ve WorkerFetchAndLock arasında sonsuz bir döngüye (Livelock) giriyor. Mesaj hiçbir zaman delivered olamıyor veya failed_permanently olarak işaretlenemiyor (EventuallyResolved kuralı ihlal edildi). *)
(* ZombieSweeper == *)
(*     (\* DB'de 'processing' statüsünde asılı kalmış bir mesaj var mı? *\) *)
(*     /\ \E m \in DOMAIN db_notifications : *)
(*         /\ db_notifications[m].state = "processing" *)
(*         (\* Etki: Durumunu tekrar pending yap *\) *)
(*         /\ db_notifications' = [db_notifications EXCEPT ![m].state = "pending"] *)
(*         (\* Etki: Outbox tablosuna tekrar ekle (Kafka'ya yeniden enjeksiyon) *\) *)
(*         /\ db_outbox' = db_outbox \cup {m} *)
(*         /\ UNCHANGED <<kafka_topic, dlq_topic>> *)


ZombieSweeper ==
    (* DB'de 'processing' statüsünde asılı kalmış bir mesaj var mı? *)
    /\ \E m \in DOMAIN db_notifications :
        /\ db_notifications[m].state = "processing"
        /\ IF db_notifications[m].retry_count < MaxRetry
           THEN (* Zombiyi kurtar, ama ceza olarak retry_count'u artır *)
                /\ db_notifications' = [db_notifications EXCEPT
                                        ![m].state = "pending",
                                        ![m].retry_count = @ + 1]
                /\ db_outbox' = db_outbox \cup {m}
                /\ UNCHANGED <<kafka_topic, dlq_topic>>
           ELSE (* Zombi çok inatçı, DLQ'ya at ve permanently failed yap *)
                /\ db_notifications' = [db_notifications EXCEPT ![m].state = "failed_permanently"]
                /\ dlq_topic' = Append(dlq_topic, m)
                /\ UNCHANGED <<db_outbox, kafka_topic>>

RetrySweeper ==
    (* DB'de retry zamanı gelmiş 'failed_retrying' statüsünde bir mesaj var mı? *)
    /\ \E m \in DOMAIN db_notifications :
        /\ db_notifications[m].state = "failed_retrying"
        /\ db_notifications' = [db_notifications EXCEPT ![m].state = "pending"]
        /\ db_outbox' = db_outbox \cup {m}
        /\ UNCHANGED <<kafka_topic, dlq_topic>>
Next == 
    \/ ( \E m \in MessageIDs : ApiCreateNotification(m))
    \/ RelayProcessOutbox
    \/ WorkerFetchAndLock
    \/ WorkerIdempotencySkip
    \/ WorkerProcessSuccess
    \/ WorkerProcessFail
    \/ WorkerProcessPermanentFail
    \/ WorkerRateLimited
    \/ ZombieSweeper
    \/ RetrySweeper

Spec == Init /\ [][Next]_vars

(* --- ADALET (FAIRNESS) KURALLARI --- *)
Fairness == 
    /\ WF_vars(RelayProcessOutbox)
    /\ WF_vars(WorkerFetchAndLock)
    /\ WF_vars(WorkerIdempotencySkip)
    /\ WF_vars(ZombieSweeper)
    /\ WF_vars(RetrySweeper)
    /\ WF_vars(WorkerRateLimited)
    /\ SF_vars(WorkerProcessSuccess)
    /\ SF_vars(WorkerProcessFail)
    /\ SF_vars(WorkerProcessPermanentFail)

(* Spec tanımımızı Fairness içerecek şekilde güncelliyoruz: *)
FairSpec == Init /\ [][Next]_vars /\ Fairness


(* --- GÜVENLİK (SAFETY) ÖZELLİKLERİ --- *)

(* 1. Teslim Edilen Mesaj Değişmez (Immutable Delivered State) *)
(* Eğer bir mesaj "delivered" olduysa, bir sonraki adımda hala "delivered" kalmalıdır. *)
DeliveredIsStable ==
    [][ \A m \in DOMAIN db_notifications :
        (db_notifications[m].state = "delivered") => (db_notifications'[m].state = "delivered") 
      ]_vars

(* 2. Kalıcı Hata Değişmez (Immutable Permanent Fail) *)
FailedIsStable ==
    [][ \A m \in DOMAIN db_notifications :
        (db_notifications[m].state = "failed_permanently") => (db_notifications'[m].state = "failed_permanently")
      ]_vars

(* --- CANLILIK (LIVENESS) ÖZELLİKLERİ --- *)

(* Sisteme giren her mesajın nihai bir sonuca ulaşma garantisi. *)
EventuallyResolved ==
    \A m \in MessageIDs :
        (m \in DOMAIN db_notifications /\ db_notifications[m].state = "pending")
        ~> 
        (* [DÜZELTME]: Q durumunu kontrol etmeden önce 'm' DB'de var mı diye bak! *)
        (m \in DOMAIN db_notifications /\ db_notifications[m].state \in {"delivered", "failed_permanently"})

====
