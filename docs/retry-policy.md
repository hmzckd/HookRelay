# HR-011 — teslimat tekrar deneme politikası

Bu belge v0.3'ün çalışan worker karar kuralını tanımlar. Başarısız girişimin sonucu ve teslimatın `retry_wait` veya `dead` durumu aynı veritabanı transaction'ında kaydedilir. `retry_wait` işinin `next_attempt_at` zamanı kalıcıdır; worker yalnızca veritabanı saatine göre zamanı gelmiş işi yeniden alır. Önceki v0.2 sürümünden kalan `failed` işler geçmiş kayıt olarak terminal kalır.

Bir teslimat için en çok 5 toplam HTTP girişimi yapılır. İlk kabulden itibaren yaş sınırı 15 dakikadır. Yeniden deneme aralığı girişim sayısına göre 1, 2, 4, 8 saniyelik üstel üst sınırlardan **full jitter** ile `[0, üst sınır]` arasından seçilir; genel üst sınır 60 saniyedir. Deneme saati ve jitter kaynağı testlerde değiştirilebilir. Sonraki zaman yaş sınırına eşit veya daha geçse iş `dead` olur.

| Sonuç | Karar |
| --- | --- |
| `2xx` ve yanıt sınırı içinde | `succeeded` |
| DNS/ağ hatası, timeout, yanıt okuma hatası | Bütçe varsa `retry_wait` |
| HTTP `408`, `429`, `5xx` | Bütçe varsa `retry_wait` |
| Diğer `4xx`, `3xx`, izin verilmeyen hedef, eksik anahtar, bozuk istek, TLS sertifika doğrulama hatası, aşırı büyük yanıt | `dead` |

Geçerli `Retry-After` değeri delta saniye veya HTTP tarihi olabilir. HTTP yanıtı yeniden denenebilir durumdaysa bu tarih **en erken** deneme zamanıdır; jitter'dan daha geçse o uygulanır. Geçmiş tarih bekleme eklemez; bozuk değer jitter'a düşer. Çok büyük bir sayısal değer taşma olmadan yaş sınırı dışı kabul edilir. Bekleme yaş sınırına yetişmiyorsa erken gönderim yapılmaz ve `dead` kararı verilir.

HTTP isteği alıcıya ulaşıp yanıt okuma hatası yaşanırsa alıcının yan etkisi belirsizdir. Politika bu durumu tekrar denemeye uygun sayar; HR-016'daki demo alıcısı `delivery_id` ile kendi kalıcı yan etkisini tekilleştirir. Bu yalnızca o alıcının davranışıdır. Worker uzun süre kapalıyken 15 dakikayı aşan `pending` veya `retry_wait` işleri, sonraki taramada `dead` ve `age_exhausted` olur. HR-014 ile süresi dolmuş `processing` işleri yeniden alınır; önceki girişim `unknown` olarak kapanır ve beş denemelik bütçeden düşer. Yaş veya deneme sınırı dolmuşsa yeni HTTP isteği yapılmaz.

Doğrulama: Sahte saat ve belirlenmiş jitter ile `500`, `408`, `429`, HTTP tarihli `503`, bozuk/çok büyük `Retry-After`, kalıcı hata, deneme limiti ve yaş sınırı tablo testleri geçti. Gönderici testi `Retry-After` başlığını ve DNS sınıflandırmasını doğruladı. Gerçek PostgreSQL testleri iki `500` sonrası başarıyı, yeniden bağlantıdan sonra ileri tarihli işin beklemesini, `400` terminal durumunu, beş girişim sınırını ve yaş aşımını doğruladı. Ayrı süreçlerle yapılan demo üç imzalı girişimi `500, 500, 204` olarak kaydetti.
