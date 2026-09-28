# HookRelay

HookRelay, olayları kabul edip kayıtlı hedeflere imzalı webhook gönderen bir Go servisidir. API, olay ve hedef başına teslimat işlerini PostgreSQL'de tek transaction'la kaydeder; worker'lar bu işleri paylaşır, [sınırlı tekrar deneme](docs/retry-policy.md) yapar ve her girişimin sonucunu tutar. Süreç kaybından sonra iş yeniden alınabilir; demo alıcısı aynı `delivery_id` için yerel yan etkiyi tekilleştirir. [Nasıl çalışır?](docs/project-overview.md) · [Güncel mimari](docs/architecture.md) · [HTTP sözleşmesi](api/openapi.yaml).

**Yerel v0.8/HR-025–026 tamamlandı.** Aynı image, Docker Compose veya kind üzerinde API, worker, migration ve demo alıcıları olarak çalışır. kind'da iki worker, Pod kaybı, API rollout ve başarısız migration kapısı denendi. [Compose rehberi](docs/compose.md) · [kind rehberi](docs/kind.md) · [CI ve yerel kontrol](docs/ci.md) · [görev ve kanıtlar](docs/tasks.md). v1.0 temiz kurulum işi sürüyor; yayımlanmış sürüm etiketi veya bulut kurulumu yoktur.

## Çalıştırma yolları ve gereksinimler

- **Yerel Go:** Bu ortamda Go 1.27.1 ve PostgreSQL 17 ile doğrulandı. Kendi geliştirme veritabanınız gerekir; entegrasyon testleri için ayrı DB'de `CREATE SCHEMA` yetkisi gerekir. API ve demo alıcıları bu modda loopback adresinde dinler.
- **Docker Compose:** Docker Desktop Linux container motoru gerekir. Go veya PostgreSQL'i hosta kurmak gerekmez; image Go'yu kendi build aşamasında kullanır.
- **kind/Kubernetes:** Docker Desktop Linux motoru ve `scripts/install-kind-tools.ps1` ile indirilen yerel kind/kubectl gerekir. Compose ile aynı `.env.compose` anahtarlarını, fakat ayrı PostgreSQL verisini kullanır. API hostta yalnızca geçici loopback port-forward ile erişilir.

Go'yu yeni kurduysanız açık VS Code pencerelerini ve terminallerini kapatıp yeniden açın; yeni terminalde `go version` komutunu deneyin. Windows'ta Go `C:\Program Files\Go\bin\go.exe` konumunda olduğu hâlde komut bulunamıyorsa o terminal için `$env:Path += ';C:\Program Files\Go\bin'` çalıştırıp tekrar deneyin. Bu geçici komut yeni terminalde yeniden gerekebilir; normalde pencereyi yeniden açmak yeterlidir.

Yerel Go kurulumunda `hookrelay` rolünün sahibi olduğu `hookrelay` veritabanı gerekir. `DATABASE_URL` için `postgres://hookrelay:PAROLA@127.0.0.1:5432/hookrelay?sslmode=disable` biçimini kullanın; paroladaki `@`, `:`, `/` gibi URL karakterleri yüzde kodlaması gerektirir. `sslmode=disable` yalnızca loopback geliştirme bağlantısı içindir. Bağlantı dizesini ve anahtarları ekrana ya da loga yazdırmayın.

Proje klasöründe `.env.example` dosyasını `.env` olarak kopyalayın. `.env` içindeki `DATABASE_URL` değerine kendi bağlantınızı girin. `PRODUCER_TOKEN`, `ADMIN_TOKEN`, `DEMO_A_SECRET` ve `DEMO_B_SECRET` değerlerini birbirinden farklı, en az 32 karakterlik rastgele değerlerle değiştirin. PowerShell'de 32 baytlık rastgele değer üretmek için `[Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(32))` kullanılabilir. `.env` Git tarafından yok sayılır; gerçek değerleri başka dosyaya koymayın. Yerel alıcıları kullanmak için `TARGET_PROFILE=demo` açıkça bulunmalıdır; değişken yoksa varsayılan `public` profilinde yerel HTTP istisnası kapalıdır. Eski `.env` kullanıyorsanız ayrı `ADMIN_TOKEN=` ve `TARGET_PROFILE=demo` satırlarını ekleyin.

VS Code'da proje klasöründe **her yeni PowerShell terminalinde** önce ayarları yükleyin:

```powershell
. .\scripts\load-env.ps1
```

İlk terminalde migration'ları uygulayıp API'yi başlatın:

```powershell
go run ./cmd/migrate
go run ./cmd/api
```

Migration komutu varsayılan olarak çalışma klasöründeki `migrations/` içeriğini sırayla uygular. Her dosya tek SQL ifadesidir; uygulandıktan sonra dosya değişirse checksum kontrolü işlemi durdurur. İlk kurulumda iki **yalnızca demo** hedefi eklenir:

Yerel Go süreçlerinin eski kodu çalışıyorsa ilgili terminalde `Ctrl+C` ile durdurun; `go run ./cmd/migrate` komutunu yeniden çalıştırıp API ve worker'ı yeni kodla açın. Eski v0.2 `failed` işleri geçmiş kayıt olarak terminal kalır; migration onları otomatik yeniden göndermez.

| Hedef | ID | Adres |
| --- | --- | --- |
| demo-a | `11111111-1111-4111-8111-111111111111` | `http://127.0.0.1:18080/hook` |
| demo-b | `22222222-2222-4222-8222-222222222222` | `http://127.0.0.1:18081/hook` |

İkinci terminalde demo alıcısını, üçüncü terminalde worker'ı başlatın. Alıcı da `.env` içindeki `DATABASE_URL` bağlantısını kullanır; önce migration'ları uygulayın:

```powershell
. .\scripts\load-env.ps1
$env:DEMO_RECEIVER_SECRET = $env:DEMO_A_SECRET
go run ./cmd/demo-receiver
```

Tekrar denemeyi canlı görmek için ilk alıcıyı `go run ./cmd/demo-receiver -mode flaky` komutuyla başlatın. Bu mod doğrulanmış ilk iki isteğe `500`, üçüncü isteğe `204` döndürür.

Yanıt kaybını ve alıcı tekilleştirmesini görmek için alıcıyı `go run ./cmd/demo-receiver -mode drop-after-commit` ile başlatın. İlk imzalı isteğin işlemi veritabanına yazılır ama bağlantı yanıt verilmeden kapanır. Worker yeniden gönderir; alıcı aynı `delivery_id` kaydını bulup `204` döner. `demo_receiver_effects` tablosunda bu teslimat için tek satır kalır. Bu tablo yerel eğitim demosunda HookRelay ile aynı PostgreSQL veritabanındadır; gerçek bir alıcı kendi kalıcı deposunda aynı benzersizlik kuralını uygulamalıdır.

Bu denemede yalnızca ilk alıcıyı açmak yeterlidir. Aşağıdaki olay kabulü adımlarında `-InFile '.\examples\order-created.json'` yerine [tek hedefli örneği](examples/order-created-demo-a.json) kullanın. Aldığınız `delivery_id` için pgAdmin'de `SELECT delivery_id, applied_at FROM demo_receiver_effects WHERE delivery_id = 'TESLIMAT_ID';` sorgusu tek satır göstermelidir; girişim geçmişinde ilk sonuç geçici hata, sonraki sonuç `204` olur.

```powershell
. .\scripts\load-env.ps1
go run ./cmd/worker
```

İki hedefi birden denemek için dördüncü terminalde ikinci alıcıyı açın:

```powershell
. .\scripts\load-env.ps1
$env:DEMO_RECEIVER_SECRET = $env:DEMO_B_SECRET
go run ./cmd/demo-receiver -listen 127.0.0.1:18081 -key-id demo/b-v1
```

`demo` profilinde worker tabloda yazılı iki tam localhost adresine gönderir; yönetici aynı adreslere yeni hedef kaydedebilir. Dış hedefler yalnızca HTTPS/443 ile çalışır ve geçerli bir sürümlü anahtar dosyası gerektirir. Yönlendirme veya ortam proxy'si izlenmez. [Ağ tehdit modeli](docs/target-security.md) bağlantı kurallarını açıklar.

## Hedef yönetimi

Yönetici anahtarı üretici anahtarından ayrıdır. Aşağıdaki örnekte yeni hedef demo-a alıcısına bağlanır ve dönen ID olay isteğinin `endpoint_ids` listesinde kullanılabilir:

```powershell
. .\scripts\load-env.ps1
$adminHeaders = @{ Authorization = "Bearer $env:ADMIN_TOKEN" }
$target = Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8080/v1/endpoints' -Headers $adminHeaders -ContentType 'application/json' -Body '{"name":"managed-a","url":"http://127.0.0.1:18080/hook"}'
$target.id
Invoke-RestMethod -Method Get -Uri 'http://127.0.0.1:8080/v1/endpoints?limit=20&offset=0' -Headers $adminHeaders
$change = @{ expected_version = $target.version; enabled = $false } | ConvertTo-Json -Compress
Invoke-RestMethod -Method Patch -Uri "http://127.0.0.1:8080/v1/endpoints/$($target.id)" -Headers $adminHeaders -ContentType 'application/json' -Body $change
```

`GET /v1/endpoints/{id}` tek hedefi okur. `PATCH` içinde mevcut `expected_version` ve `url`, `key_id` veya `enabled` bulunmalıdır; eski sürümle değişiklik `409` döndürür. URL, anahtar kimliği veya etkinlik durumu değişirse sürüm artar. Kabul edilmiş teslimatın URL ve anahtar referansı kaydedildiği anda sabitlenir. Kapatılan hedef yeni olaylarda kullanılamaz; bekleyen işler `endpoint_disabled` nedeniyle sonlanır. Başlamış bir HTTP isteği tamamlanabilir; kapalıyken terk edilmiş girişim `unknown` kaydıyla sonlanır. Yeniden açmak eski sonlandırılmış işleri diriltmez. API anahtar kimliğini gösterir, gizli anahtar değerini göstermez. Dış HTTPS hedefi hazırlama ve döndürme adımları [anahtar rehberindedir](docs/key-rotation.md).

Yönetici anahtarıyla `GET /v1/metrics` kuyruk yaşı/sayısı, teslimat ve girişim durumları, süre eşikleri ve API veritabanı havuzunu metin olarak verir. Worker loglarında `delivery_id` ile ilgili gönderim ve kalıcı sonuç satırları bulunur. Komut ve teşhis adımları [gözlemlenebilirlik rehberindedir](docs/observability.md).

API'nin `GET /livez` kontrolü süreç çalışırken `204` döner; `GET /readyz` PostgreSQL'e ve migration uygulanmış `events` tablosuna erişebiliyorsa `204`, erişemiyorsa `503` döner. DB başlangıçta kapalı olsa bile API açılır, hazır olmadığını bildirir ve olay kabulü sahte başarı döndürmez. Worker DB hatalarında 0,5–5 saniye arasında artan beklemeyle yeniden dener. İsteğe bağlı sağlık sunucusunu worker terminalinde `go run ./cmd/worker` öncesi `$env:WORKER_HEALTH_ADDR='127.0.0.1:8082'` ile açabilirsiniz. `http://127.0.0.1:8082/livez`, `/readyz` ve `/statusz` sırasıyla süreç, son başarılı DB yoklaması ve ilerleme özetini verir. İkinci worker için farklı bir loopback portu seçin. [Kesinti denemesi ve sınırlar](docs/health.md).

İsterseniz başka bir terminalde `. .\scripts\load-env.ps1` ve `go run ./cmd/worker` komutlarını tekrar çalıştırarak ikinci worker'ı açabilirsiniz. Her süreç varsayılan olarak aynı anda en çok **4** teslimat işler; `.env` içindeki `WORKER_CONCURRENCY` değeriyle 1–16 arasında ayarlayabilirsiniz. Worker boş kapasitesi kadar iş alır. `Ctrl+C` sonrası yeni iş almaz, başlamış gönderimlere en çok 10 saniye tanır. Bu sürede sonuç alınamazsa işi kalıcı başarısızlık saymaz; lease süresi sonunda başka worker kurtarır. PostgreSQL iş başına 15 saniyelik sahiplik süresi tutar. İlk HTTP isteği alıcıya ulaşmış olabilir; bu durumda ikinci isteğin yan etkisi de oluşabilir. [Kapanış davranışı](docs/releases/v0.4-hr015.md) ve [kesinti noktaları](docs/releases/v0.4-hr014.md) ayrıca açıklanır.

## Olay kabulü

Başka bir PowerShell terminalinde aynı `.env` ayarlarını yükleyin. Tüm `/v1/` istekleri `Authorization: Bearer <token>` ister. İki hedefli örnek istek [examples/order-created.json](examples/order-created.json) dosyasında bulunur; iki alıcı da çalışıyor olmalıdır.

```powershell
. .\scripts\load-env.ps1
$headers = @{ Authorization = "Bearer $env:PRODUCER_TOKEN" }
$headers['Idempotency-Key'] = [guid]::NewGuid().ToString()
$event = Invoke-RestMethod -Method Post -Uri 'http://127.0.0.1:8080/v1/events' -Headers $headers -ContentType 'application/json' -InFile '.\examples\order-created.json'
$event.id
Invoke-RestMethod -Method Get -Uri "http://127.0.0.1:8080/v1/events/$($event.id)" -Headers $headers
$deliveryId = $event.deliveries[0].id
Invoke-RestMethod -Method Get -Uri "http://127.0.0.1:8080/v1/deliveries/$deliveryId" -Headers $headers
Invoke-RestMethod -Method Get -Uri "http://127.0.0.1:8080/v1/deliveries/$deliveryId/attempts?limit=20&offset=0" -Headers $headers
```

Başarılı kabul `202 Accepted` ve `Location: /v1/events/{id}` döndürür. Bu yanıt, olayın veritabanına yazıldığını gösterir; teslimatı beklemek için aynı `id` ile `GET` yapın. İş `pending → processing → succeeded` olabilir; geçici hatada `retry_wait` ile sonraki zamanını bekler, kalıcı hata veya biten bütçede `dead` olur. Çöken girişim, geçmişte `unknown` olur. En çok 5 toplam girişim ve 15 dakika yaş sınırı vardır. Aynı anahtar ve aynı içerikle `POST` tekrarlandığında yine `202`, aynı olay ID'si ve `Idempotency-Replayed: true` döner; yeni teslimat açılmaz. Aynı anahtar farklı içerikle kullanılırsa `409 idempotency_conflict` alınır. Yeni bir olay için yeni anahtar üretin. [İmza sözleşmesi](docs/delivery-semantics.md) ve [tekrar deneme politikası](docs/retry-policy.md) sınırları açıklar.

`Idempotency-Key` zorunlu, tek bir 1–128 karakterlik ASCII tokendır; ilk karakter harf/rakam, kalanlar harf/rakam/nokta/alt çizgi/iki nokta/tire olabilir. Anahtar tek yerel üretici kapsamında saklanır. Karşılaştırma olay türünü, `payload` içindeki **aynen kaydedilen JSON byte'larını** ve sıralanmış hedef ID listesini kapsar. Hedef sırası değişebilir; `payload` içindeki boşlukların değişmesi farklı içerik sayılır. Anahtar en az 30 gün korunur; süre dolsa bile olayın tüm işleri bitmeden silinmez. Temizlik çalışıp uygun olayı sildikten sonra aynı anahtar yeni olay açabilir. [Saklama ve geri yükleme rehberi](docs/retention-backup.md) ile [HR-010 geliştirme kaydı](docs/releases/v0.3-hr010.md) ayrıntıları verir.

İstek en çok 256 KiB olabilir. `type` bir harfle başlayıp yalnızca ASCII harf/rakam/nokta/alt çizgi/tire içerir ve en çok 128 karakterdir. `payload` JSON nesnesidir. `endpoint_ids` içinde 1–10 farklı ve mevcut etkin hedef ID'si bulunur. İstek yanlışsa hiçbir olay veya teslimat işi oluşmaz. HTTP sözleşmesinin tamamı [api/openapi.yaml](api/openapi.yaml) dosyasındadır.

API bir süreçte olay kabulünü saniyede 20, en çok 20 ani istekle; yönetici değişikliklerini saniyede 5, en çok 5 ani istekle sınırlar. Aşımda `429` ve `Retry-After: 1` döner. Worker aynı hedefe süreç başına saniyede 5 gönderim, en çok 2 ani gönderim yapar; aynı anda en fazla `WORKER_CONCURRENCY` işi yürütür. Birden çok API/worker süreci bu yerel sınırları ayrı uygular; sistem genelinde ortak kota iddiası yoktur.

## Doğrulama

```powershell
go test ./...
go vet ./...
```

Gerçek PostgreSQL entegrasyon testlerini çalıştırmak için yalnızca teste ayrılmış bir DB URL'si verin:

```powershell
$env:TEST_DATABASE_URL = 'postgres://kullanici:parola@127.0.0.1:5432/hookrelay_test?sslmode=disable'
go test -count=1 ./...
```

Testler benzersiz isimli geçici şemalar açıp sonunda yalnızca kendi şemalarını kaldırır. Olay/iş transaction'ını, worker sahipliğini, kapasite/kapanış sınırını, süreç çökmesinden kurtarmayı, imzayı, yanıt kaybını ve alıcıdaki kalıcı tekilleştirmeyi dener. `TEST_DATABASE_URL` yoksa veritabanı testleri atlanır; sonuç tam entegrasyon doğrulaması değildir. Kullanıcının `hookrelay_test` veritabanı bu amaçla kullanılabilir. Gerçek test parolası yalnızca kendi yerel `.env` veya terminal ayarlarınızda kalır.

## Saklama ve yedek doğrulaması

`.env` yüklü ayrı bir terminalde `go run ./cmd/cleanup` uygun eski olayların sayısını gösterir ve veri silmez. Yedeği aldıktan sonra `go run ./cmd/cleanup -apply -limit 100` komutu en fazla 100 uygun olayı bağlı geçmişiyle temizler. Şu an temizliği otomatik başlatan bir zamanlayıcı yoktur. `./scripts/verify-backup-restore.ps1` tam yedeği geçici, ayrı ve boş bir PostgreSQL veritabanına geri yükleyip geçmiş kayıt sayılarını karşılaştırır; ana veritabanına geri yükleme yapmaz. Ön koşullar ve geri dönüş sınırları [rehberde](docs/retention-backup.md) açıklanır.

Küçük yerel yük deneyi için `.env` yüklüyken `.\scripts\measure-load.ps1` komutunu çalıştırın. Bu deney yalnızca `hookrelay_test` içindeki geçici şemaları kullanır ve 1/4 worker süreçlerinin sonuçlarını verir. Port, yöntem ve sonuçlar [yük deneyi raporundadır](docs/load-test.md).

## Docker ile çalıştırma

Docker Desktop'ın Linux container motoru açıksa proje klasöründe:

```powershell
.\scripts\init-compose-env.ps1
docker compose --env-file .env.compose up --build -d
.\scripts\smoke-compose.ps1
```

İlk komut yalnızca `.env.compose` yoksa çalıştırılır. Compose kendi PostgreSQL volume'unu kullanır; yukarıdaki `.env` ve yerel PostgreSQL kurulumundan ayrıdır. Kapatmak için `docker compose --env-file .env.compose down` çalıştırın; veriler korunur. Ayrıntılar ve yeniden açma adımı [Compose rehberindedir](docs/compose.md).

## Yerel Kubernetes ile çalıştırma

Docker Desktop Linux motoru açıkken Compose'u ve `8080` portunu kullanan yerel API'yi durdurun. Proje kökünde:

```powershell
if (-not (Test-Path .env.compose)) { .\scripts\init-compose-env.ps1 }
docker compose --env-file .env.compose build api
.\scripts\install-kind-tools.ps1
.\scripts\kind-up.ps1
.\scripts\smoke-kind.ps1
```

kind kendi PostgreSQL PVC'sini kullanır; Compose volume'u veya yerel Go veritabanıyla veri paylaşmaz. Sonraki açılış, arıza deneyi ve cluster silme/veri sınırı [kind rehberindedir](docs/kind.md).

## Sınırlar

Bu sürüm ayrı yönetici/üretici anahtarlı yerel denemedir; host erişimi yalnızca loopback veya kind port-forward üzerinden yapılır. Bearer token düz HTTP üzerinden yalnızca yerel geliştirme içindir. Doğrudan Go modunda dış anahtar dosyası yapılandırılırsa worker izin verilen dış HTTPS hedeflerine çıkabilir; mevcut Compose/kind demoları yalnızca iki yerel alıcı için yapılandırılmıştır. Gerçek bir internet alıcısına gönderim bu geliştirme ortamında denenmedi. Çöken worker'ın `processing` işi sahiplik süresi dolunca kurtarılır; alıcının ilk isteği işleyip işlemediği HookRelay tarafından kesin bilinmez. Demo alıcısı kendi kaydını tekilleştirir; bu başka alıcıların yan etkileri veya sistem genelinde exactly-once garantisi değildir. Geri yükleme denemesi doğrudan Go modundaki DB içindir; otomatik yedekleme, yüksek erişilebilir DB ve üretim işletimi kapsamda değildir.
