# Teslimat ve imza sözleşmesi (v0.8)

Bu sayfa çalışan v0.8 davranışını anlatır. İlk tek worker kararı [ADR 002](decisions/002-single-worker-local-delivery.md) içinde tarihsel kayıt olarak tutulur; bugünkü çok worker kararı [ADR 003](decisions/003-multi-worker-at-least-once.md) içindedir. [Retry politikası](retry-policy.md) ve [anahtar döndürme rehberi](key-rotation.md) ayrıntı verir.

`demo` profilinde iki sabit localhost alıcısı; her iki profilde yapılandırılmış dış HTTPS hedefi kullanılabilir. API `202` yanıtını olay, işler ve idempotency anahtarı PostgreSQL'e birlikte yazıldıktan sonra verir. Worker'lar ayrı süreçlerdir; her claim bir girişim kaydı oluşturur. Başarılı sonuç, HookRelay'in alıcıdan `2xx` HTTP yanıtı gözlemlemesi ve yanıt sınırını aşmamasıdır. Alıcının kendi kalıcı yan etkisini kanıtlamaz.

Yeni işin yolu `pending → processing → succeeded` olabilir. Geçici hatada `retry_wait` üzerinden yeniden alınır; kalıcı hata, kapanan hedef veya biten bütçede `dead` olur. Eski v0.2'den kalan `failed` kayıtları geçmişte görülebilir, fakat yeni worker bu duruma iş bırakmaz. Claim ve girişim başlangıcı tek transaction'da; ağ çağrısı transaction dışında; girişim sonucu ve iş sonucu başka bir transaction'da kaydedilir. Birden çok worker `SKIP LOCKED` ile iş paylaşır. İşin lease süresi 15 saniyedir. Worker ölürse süresi dolan `processing` işi başka worker tarafından alınır; eski girişim `unknown` olarak kaydedilir ve deneme bütçesinden düşer. En çok 5 girişim ve kabulden itibaren 15 dakika sınırı vardır. Alıcı ilk isteği işlemiş olabilir; bu nedenle exactly-once uzak yan etki veya kesin başarılı teslimat iddiası yoktur.

## İmza protokolü v1

Worker, olayın kaydedilen `payload` byte dizisini `POST /hook` gövdesi olarak gönderir. Header'lar:

| Header | Değer |
| --- | --- |
| `X-HookRelay-Event-Id` | Olay UUID'si |
| `X-HookRelay-Event-Type` | Olay türü |
| `X-HookRelay-Delivery-Id` | Tek teslimat işinin UUID'si |
| `X-HookRelay-Attempt-Id` | Bu HTTP girişiminin UUID'si |
| `X-HookRelay-Key-Id` | Demo için `demo/a-v1` veya `demo/b-v1`; dış hedef için yapılandırılmış `external-...-vN` |
| `X-HookRelay-Timestamp` | Unix saniyesi, ondalık |
| `X-HookRelay-Signature` | `sha256=` + küçük harfli hex HMAC-SHA256 |

İmzalanan byte dizisi: `timestamp + "." + key_id + "." + event_id + "." + event_type + "." + delivery_id + "." + attempt_id + "." + raw_payload`. JSON yeniden serileştirilmez. Alıcı aynı diziyi kurup HMAC'i sabit zamanlı karşılaştırır, anahtar ID'sini beklediği değerle karşılaştırır ve zaman damgasının kendi saatinden en çok 5 dakika uzakta olmasına izin verir. Saatler uyumlu olmalıdır. İmza yalnızca istek bütünlüğü/kimlik doğrulama sağlar; 5 dakikalık pencerede yeniden oynatmayı tek başına engellemez. Demo alıcısı `delivery_id` ile kalıcı tekilleştirme uygular; başka alıcı bunu kendi deposunda yapmalıdır.

Bağımsız test vektörü: anahtar 32 adet ASCII `s`; timestamp `1789992000`; anahtar ID `demo/a-v1`; olay ID `event-1`; olay türü `order.created`; teslimat ID `delivery-1`; girişim ID `attempt-1`; gövde `{"order_id":"demo-1"}`. Sonuç: `sha256=601b8f3efe5682e88daad8d112c34b1f68e7f11d728d71231021af03cea89a27`. Bu vektör kod testinde de doğrulanır.

Demo anahtarları yerel `DEMO_A_SECRET` ve `DEMO_B_SECRET` ortam değişkenlerinden; dış anahtarlar `EXTERNAL_KEY_DIR` içindeki sürümlü dosyalardan gelir. Veritabanında yalnızca `secret_ref` bulunur. İmza alıcısı ilgili anahtarı ayrı ve güvenli biçimde edinmelidir. Eksik/kısa anahtar veya izin verilmeyen hedef durumunda HTTP bağlantısı kurulmaz ve iş başarısız olur. Gerçek anahtarlar repo, log veya API yanıtında bulunmaz.

## Ağ ve hata sınırları

- Açık `demo` profilinde yalnızca `http://127.0.0.1:18080/hook` ve `http://127.0.0.1:18081/hook` yerel istisnadır; aynı adrese farklı hedef kimlikleri bağlanabilir. Her demo adresi kendi anahtar referansını gerektirir. Dış hedefler yalnızca HTTPS/443 ve yüklenmiş anahtar kimliğiyle kabul edilir. Yönlendirme ve ortam proxy'si izlenmez.
- Toplam HTTP süresi 4 saniye, bağlantı süresi 2 saniye, yanıt başlığı süresi 3 saniye ile sınırlıdır. Yanıt gövdesinden en çok 4097 bayt okunur; 4096 baytı aşan yanıt `response_too_large` olarak başarısızdır.
- `2xx` başarılıdır. `408`, `429`, `5xx`, DNS/ağ hatası ve timeout bütçe içinde yeniden denenir; diğer `4xx`, `3xx`, izin verilmeyen hedef, eksik anahtar, TLS sertifika hatası ve aşırı büyük yanıt kalıcı hatadır. [Tam karar tablosu](retry-policy.md).
- API'den `GET /v1/deliveries/{id}` ile iş; `GET /v1/deliveries/{id}/attempts?limit=20&offset=0` ile girişim sayfası okunur. Sayfa boyutu 1–100, offset 0–10000. Girişim geçmişinde `succeeded`, `failed` veya belirsiz `unknown` görülebilir.

Demo alıcısı `-mode error` ile imzayı doğruladıktan sonra `500`, `-mode slow` ile worker timeout'u oluşturabilir. Bu modlar yalnızca loopback demo ortamı içindir.
