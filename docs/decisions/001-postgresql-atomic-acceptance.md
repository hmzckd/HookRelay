# ADR 001 — PostgreSQL'de atomik olay kabulü

Durum: v0.1'de kabul edildi; v0.8'de geçerli.

Sorun: Olay verisi kalıcıyken teslimat işi kaybolursa üretici başarılı kabul alır ama iş asla gönderilemez. Ayrı bir mesaj aracısına aynı anda yazmak iki sistem arasında yeni bir tutarlılık sorunu açar.

Karar: `events`, hedef başına `deliveries` ve üretici/idempotency anahtarı aynı PostgreSQL transaction'ında yazılır. `deliveries` kalıcı iş kuyruğudur. Hedef URL'si, sürümü ve imza anahtarı referansı iş üzerinde saklanır. Başarılı commit'ten önce `202` verilmez.

Gerekçe: Tek transaction, kabul edilen olay için hedef başına iş kaydının birlikte oluşmasını sağlar. İkinci iş ekleme hatası testinde ilk iş ve olay da geri alınır.

Bedel: PostgreSQL hem uygulama verisi hem iş kuyruğu yükünü taşır; yerel küçük yük deneyi üretim ölçeğini göstermez. Uzak teslimat garantisi bu kararın sonucu değildir. Tekrar deneme ve çökme kurtarma sonraki aşamalarda eklendi; başka broker için outbox tasarımı ancak ölçülen gereksinim doğarsa ele alınır. [Güncel mimari](../architecture.md).
