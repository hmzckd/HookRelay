# ADR 003 — Çok worker ve belirsiz teslimat

Durum: v0.4'te kabul edildi; v0.8'de geçerli. [ADR 002](002-single-worker-local-delivery.md) tek worker kararının yerini aldı.

Sorun: Tek worker işleyişi yavaş hedefte kuyruğu geciktirir; süreç claim sonrası ölürse iş `processing` durumunda kalır. Ağ isteği alıcıya ulaşmışken yerel sonuç kaydı kaybolabilir.

Karar: Birden çok worker aynı PostgreSQL kuyruğundan `FOR UPDATE SKIP LOCKED` ile iş alır. Her claim atomik token, 15 saniyelik lease ve girişim satırı oluşturur. HTTP isteği transaction dışında yapılır. Lease dolunca eski girişim `unknown` olur, iş bütçe içinde tekrar sahiplenilir; eski token'ın sonucu yazılamaz. Her süreç 1–16 eşzamanlı gönderimle sınırlıdır. Demo alıcısı `delivery_id` için kalıcı benzersizlik uygular.

Gerekçe: Pod veya süreç kaybından sonra iş kaybolmaz; kısa DB transaction'ları ağ beklerken kilit tutmaz. HR-026'daki iki Pod deneyinde 24 iş 12/12 paylaşıldı; zorla Pod kaybında dört `unknown` girişimden sonra dört iş tamamlandı.

Bedel: Lease ve tekrar deneme **at-least-once girişim** davranışı verir; alıcı aynı teslimatı birden çok kez görebilir. Demo alıcısının tek yan etkisi başka alıcılar için garanti değildir. Bütçe, kalıcı hata, kapalı hedef veya veri kaybında başarı sözü yoktur. [Teslimat sınırları](../delivery-semantics.md).
