# ADR 002 — Tek worker ile yerel imzalı teslimat

Durum: v0.2 için uygulandı; v0.4'te [ADR 003](003-multi-worker-at-least-once.md) tarafından değiştirildi. Aşağıdaki karar tarihsel v0.2 davranışıdır, çalışan v0.8 yapılandırması değildir.

Sorun: Gönderim asenkron olmalı; ilk dilimde ağ, imza ve geçmiş davranışını çok worker/lease tasarımından ayrı görmek gerekir.

Karar: Ayrı `cmd/worker` süreci PostgreSQL'de advisory lock alarak tek worker kuralını korur. Bir `pending` işi transaction içinde `processing` yapar ve girişim satırı oluşturur; transaction kapandıktan sonra izinli localhost hedefine HMAC-SHA256 imzalı HTTP POST gönderir. Sonucu ikinci transaction'da iş ve girişime yazar. Demo hedefleri ile anahtar referansları sabittir.

Gerekçe: Ağ beklerken veritabanı transaction'ı açık kalmaz. Tek süreçte durum geçişleri, imza ve timeout açıkça görülebilir. İkinci worker aynı anda başlatılırsa reddedilir.

Bedel: Süreç `processing` sonrasında çökerse girişim askıda kalır. Sonuç kaydı kaybolursa alıcının ne yaptığı bilinemez. v0.3 retry, v0.4 çok worker sahipliği ve kurtarma bu bedelleri ayrı testlerle ele alır.
