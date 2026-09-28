# HR-020: loglar ve metrikler

API ve worker her satırı JSON log olarak yazar. Kabul logunda `event_id`, yeni iş sayısı ve `replayed` bulunur. Worker'ın `delivery attempt started` ve `delivery send finished` satırlarında aynı `event_id`, `delivery_id`, `attempt_id` ve `endpoint_id` yer alır; ikincisi sonuç sınıfı, HTTP kodu ve gerçek gönderim süresini de içerir. Veritabanı commit'inden sonra `delivery outcome persisted` satırı `retry_wait`, `succeeded` veya `dead` kararını ve gerekçesini gösterir. Payload, hedef URL'si, imza anahtarı, `Retry-After` değeri ve ham hata metni loglanmaz. Süreç kapanırken sonuç kalıcılaştırılamazsa veya sahiplik kaybedilirse ayrı uyarı yazılır; bu satırlar başarı anlamına gelmez.

`GET /v1/metrics` yalnızca ayrı yönetici anahtarıyla açılır. API çalışırken başka bir PowerShell terminalinde:

```powershell
. .\scripts\load-env.ps1
$headers = @{ Authorization = "Bearer $env:ADMIN_TOKEN" }
Invoke-WebRequest -Uri 'http://127.0.0.1:8080/v1/metrics' -Headers $headers | Select-Object -ExpandProperty Content
```

Yanıt [Prometheus text 0.0.4](https://prometheus.io/docs/instrumenting/exposition_formats/) biçimindedir; `Content-Type` başlığı sürümü belirtir. Yönetici dışı istek `401`, DB okunamazsa `503` döner. Maliyetli tam tablo sayımlarına karşı metrik okuması süreç başına saniyede 1, ani 2 istekle sınırlıdır; aşım `429` döner. Gerçek üretimde scrape erişimi ayrıca ağ düzeyinde kısıtlanmalı ve TLS ile korunmalıdır; yerel Go ve Compose host erişimi loopback'tedir, kind'da host erişimi geçici loopback port-forward'dır.

| Ölçüm | Anlamı |
| --- | --- |
| `hookrelay_events_stored` | Şu anda DB'de tutulan olay sayısı; idempotent replay yeni kayıt oluşturmaz. |
| `hookrelay_deliveries{status="..."}` | Sabit altı durumdaki iş sayısı; `retry_wait` yeniden gönderilmeyi bekler, `dead` terminaldir. |
| `hookrelay_attempts{status="..."}` | `processing`, `succeeded`, `failed`, `unknown` girişim geçmişi. |
| `hookrelay_retries_started` | Her işte ilkinden sonraki sahiplenmelerin toplamı; lease kurtarması da dahildir. |
| `hookrelay_queue_ready` | Şimdi işlenebilir `pending` ve zamanı gelmiş `retry_wait` sayısı. |
| `hookrelay_queue_oldest_age_seconds` | Bekleyen en eski işin yaşı; zamanı henüz gelmemiş retry de dahildir. |
| `hookrelay_attempt_duration_le{seconds="..."}`, `hookrelay_attempt_duration_count`, `hookrelay_attempt_duration_sum_seconds` | Kalıcı sonucu olan girişimlerin DB'de ölçülen süre dağılımı. `unknown` hariçtir; sayımlar tutulan geçmişe aittir. |
| `hookrelay_db_pool_connections{state="..."}`, `hookrelay_db_pool_waits_total`, `hookrelay_db_pool_wait_duration_seconds_total` | Yalnızca API sürecinin bağlantı havuzu; worker havuzu dahil değildir. |

DB kaynaklı sayımlar **gauge** tipindedir: [saklama temizliği](retention-backup.md) çalışınca azalabilirler. Süre eşiklerinin `seconds` etiketi yalnızca `0.1`, `0.5`, `1`, `2`, `5` değerlerinden oluşur; bu seri bir Prometheus histogram counter'ı değildir. API havuzu bekleme sayaçları süreç yeniden başladığında sıfırlanır. Ayrı SQL sorguları ve eşzamanlı worker işlemleri nedeniyle tek yanıtın bütün alanları atomik bir anlık görüntü değildir. Bu uç nokta canlılık/hazırlık kontrolü değildir; ayrı kontroller [sağlık rehberinde](health.md) açıklanır.

Örnek PromQL panel sorguları:

```promql
hookrelay_queue_ready
hookrelay_queue_oldest_age_seconds
hookrelay_deliveries{status="dead"}
hookrelay_attempts{status="failed"}
hookrelay_attempt_duration_le{seconds="1"} / scalar(clamp_min(hookrelay_attempt_duration_count, 1))
rate(hookrelay_db_pool_waits_total[5m])
```

Süre oranı tutulan **tüm** geçmişin payıdır; son beş dakikanın gecikme yüzdesi değildir. `attempt_duration` claim'den kalıcı sonuca kadar geçen DB süresidir; tek tek ağ gönderiminin daha dar süresi worker logundaki `send_duration_seconds` alanıdır. Metrikler yalnızca sabit durum/eşik/havuz etiketlerini kullanır; olay veya teslimat ID'si, hedef URL'si, anahtar ya da kullanıcı verisi etiket değildir.

Bir teslimat geciktiğinde önce `queue_ready` ve `queue_oldest_age_seconds` değerlerine bakın. İkisi yükseliyorsa worker'ın çalıştığını, `delivery attempt started` satırlarının gelip gelmediğini ve DB havuzu beklemelerini kontrol edin. `retry_wait` yükseliyorsa `hookrelay_attempts{status="failed"}` ve worker sonuç logundaki `failure_class` değerini inceleyin. Bir `delivery_id` biliniyorsa üretici anahtarıyla `GET /v1/deliveries/{id}` ve `GET /v1/deliveries/{id}/attempts` çağrılarını yapın; `terminal_reason`, HTTP kodu ve başarısızlık sınıfı nedeni gösterir. Aynı kimliği logda arayarak girişimin `event_id` ve `attempt_id` değerlerine ulaşın. `unknown` geçmişi, eski worker'ın sonucunun bilinmediği ve tekrar gönderimin mümkün olduğu anlamına gelir; alıcı tekilleştirmesi önemini korur.
