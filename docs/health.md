# HR-021: canlılık, hazırlık ve DB kesintisi

API'de `GET /livez` yalnızca HTTP sürecinin çalıştığını bildirir (`204`); PostgreSQL sorgulamaz. `GET /readyz` 500 ms sınırında `events` tablosunu ve bu sürümün son zorunlu migration kaydını kontrol eder. Bağlantı veya migration eksikse `503 not ready`, sorgu başarılıysa `204` döner. Uç noktalar anahtarsızdır: doğrudan Go çalıştırmasında loopback'te, Compose/kind içinde ise yalnızca iç ağda ve host loopback erişimiyle kullanılır. DB başlangıçta kapalı olsa bile API dinlemeye başlar; `/readyz` başarısızdır ve yeni olay isteği `503 storage_unavailable` döner. Bağlantı dönünce süreç yeniden başlatılmadan hazırlık ve kabul geri gelir.

Worker'ın isteğe bağlı sağlık sunucusu ayrı loopback portunda çalışır. Bir worker terminalinde:

```powershell
. .\scripts\load-env.ps1
$env:WORKER_HEALTH_ADDR = '127.0.0.1:8082'
go run ./cmd/worker
```

Başka terminalde `Invoke-WebRequest http://127.0.0.1:8082/livez`, `.../readyz` ve `Invoke-RestMethod http://127.0.0.1:8082/statusz` ile bakın. `WORKER_HEALTH_ADDR` boşsa sunucu açılmaz. İkinci worker için `127.0.0.1:8083` gibi ayrı port kullanın. `0.0.0.0` ve dış adresler reddedilir.

Worker `/livez` sürece bağlıdır; DB kesilince `204` kalır. `/readyz` başarılı bir DB yoklaması görülene kadar `503` döner; son başarılı yoklama 10 saniyeden eskiyse, çözülmemiş depolama hatası varsa veya kontrollü kapanış başladıysa yine `503` döner. `/statusz` aynı hazırlık durumunu, son başarılı yoklama zamanını, son girişim bitişini, devam eden iş sayısını ve süreçte görülen depolama hata sayısını JSON olarak verir. Son girişim bitişi sonucun kalıcı yazıldığını tek başına kanıtlamaz; kalıcı sonucu teslimat/geçmiş API'sinden okuyun. Boş kuyrukta başarılı yoklama da ilerlemedir. Bu uç noktalar yalnızca yerel geliştirme içindir; genel ağa açılmamalıdır.

DB hatasında worker süreçten çıkmaz. Her eşzamanlı çalışma döngüsü 0,5 saniyeden başlayıp en fazla 5 saniyeye çıkan beklemeyle yeniden dener. DB geri dönünce bekleme sıfırlanır ve kalıcı `pending`/`retry_wait` işleri işlenir. Gönderimden sonra DB kesilirse sonuç kaydedilemeyebilir; lease dolduğunda girişim `unknown` olarak kurtarılır ve aynı teslimat tekrar gönderilebilir. Alıcının `delivery_id` tekilleştirmesi bu yüzden gereklidir. Hatalı yapılandırma, geçersiz port veya açılmayan sağlık dinleyicisi kalıcı başlangıç hatasıdır ve süreçten çıkılır.

Kesinti davranışı, PostgreSQL hizmetini durdurmadan yalnızca geçici test şemasının bağlantısını kesen yerel TCP proxy ile doğrulanır. `TEST_DATABASE_URL` ayarlandıktan sonra şu test API kabulünün kesintide başarısız olduğunu, worker'ın açık kaldığını, hazırlığın düştüğünü ve bağlantı geri gelince aynı süreçte kuyruğun tamamlandığını kontrol eder:

```powershell
go test -count=1 ./internal/postgres -run TestDatabaseOutageRejectsAcceptanceAndWorkerRecoversWithoutRestart
```

Bu test bir Kubernetes dağıtımı veya gerçek restart yöneticisi çalıştırmaz; kanıtladığı şey DB kesintisinin çalışan API/worker süreçlerini kapatmadığıdır. Ayrı [yerel kind deneyi](releases/v0.8-hr026.md) Pod kaybı ve API rollout'unu doğrular; Kubernetes üzerinde DB kesintisi ayrıca tekrarlanmadı. `livez` DB'ye bağlanmadığı için geçici DB kesintisini yeniden başlatma gerekçesine dönüştürmez.
