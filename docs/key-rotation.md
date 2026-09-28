# Dış hedef anahtarı hazırlama ve döndürme

Bu rehber doğrudan Go süreçleri için `.env` ve yerel `EXTERNAL_KEY_DIR` kullanır. Mevcut Compose/kind manifestleri yalnızca iki demo anahtarını besler; dış anahtar dosyası mount'u ve gerçek internet alıcısı deneyi kurulmamıştır. Aşağıdaki dış hedef adımlarını bu container demolarına uygulanmış saymayın.

HookRelay yalnızca `external-<ad>-v<pozitif sürüm>.key` biçimindeki dosyalardan 32–128 **ham bayt** okur. Dosya adı imza `key_id` değeridir; baytlar ne API yanıtına ne PostgreSQL'e yazılır. En çok 64 dış anahtar yüklenir. Kaynak kontrolü dışında, yalnızca servis hesabının okuyabildiği bir klasör kullanın. Windows'ta klasörün Güvenlik sekmesinden erişimi kısıtlayın; Unix'te dosyalara `0600`, klasöre `0700` izni verin. `.secrets/` Git tarafından yok sayılır.

Yerel PowerShell örneği, proje klasöründe:

```powershell
New-Item -ItemType Directory -Path .secrets -Force | Out-Null
$keyPath = Join-Path (Resolve-Path .secrets).Path 'external-shop-v1.key'
[IO.File]::WriteAllBytes($keyPath, [Security.Cryptography.RandomNumberGenerator]::GetBytes(32))
```

`.env` içine `EXTERNAL_KEY_DIR=.secrets` ekleyin. `TARGET_PROFILE` belirtilmezse `public` kullanılır; bu profil yerel HTTP demo adreslerini reddeder. Önceki yerel örnekleri çalıştırmak için `TARGET_PROFILE=demo` açıkça kalabilir; bu profilde de dış HTTPS hedefleri aynı kurallarla kullanılabilir. API ve worker anahtar dosyalarını **başlangıçta** okur; dosya eklendiğinde veya kaldırıldığında ikisini de yeniden başlatın. Worker hedefe yalnızca HTTPS/443 ile bağlanır. Alıcının geçerli TLS sertifikası ve aynı ham gizli anahtarı güvenli bir kanaldan edinmiş olması gerekir.

Anahtar yüklendikten sonra yönetici hedefi `POST /v1/endpoints` ile `{"name":"shop","url":"https://hooks.example.com/hook","key_id":"external-shop-v1"}` gövdesiyle oluşturabilir. Sorgu parametresi, kullanıcı bilgisi ve özel ağ URL'leri reddedilir. `key_id` API'de görülebilir; `.key` dosyasının içeriği görünmez. Üretici bu yeni hedef ID'sini olayın `endpoint_ids` listesine koyar.

Döndürme sırası:

1. Yeni `external-shop-v2.key` dosyasını ayrı rastgele baytlarla oluşturun. Alıcıyı geçiş sırasında **v1 ve v2** imzalarını doğrulayacak şekilde hazırlayın; `delivery_id` tekilleştirmesini koruyun.
2. v1 dosyasını tutarak API ve worker'ı yeniden başlatın. Yönetici `GET /v1/endpoints/{id}` sonucundaki sürümü alır ve `PATCH` gövdesinde `{"expected_version":1,"key_id":"external-shop-v2"}` gönderir. Güncel sürüm farklıysa yeni sürümle tekrar deneyin.
3. Önceden kabul edilen teslimatlar v1, yeni teslimatlar v2 kimliğiyle imzalanır. Eski anahtarın aktif işi kalmadığını PostgreSQL'de `SELECT count(*) FROM deliveries WHERE secret_ref = 'external-shop-v1' AND status IN ('pending','retry_wait','processing');` sorgusuyla doğrulayın. Sayı sıfır olana ve olası alıcı tekrarları geçene kadar iki anahtarı da tutun. Yedekten eski durum geri yüklenirse v1 teslimatları yeniden ortaya çıkabilir.
4. Geçiş tamamlanınca v1 dosyasını kaldırın, API/worker'ı yeniden başlatın ve alıcının v1 doğrulamasını kapatın. Dosyayı erken kaldırmak eski bekleyen işleri `secret_unavailable` ile sonlandırabilir.

Yükleme, URL veya anahtar hatasında API/worker yalnızca sabit hata türü kaydeder; veritabanı bağlantı dizesi, gizli anahtar baytları ve ham hedef URL'si loga konmaz. API olay kabulü süreç başına saniyede 20 (ani 20), yönetici değişikliği saniyede 5 (ani 5) ile sınırlandırılır; aşım `429` ve `Retry-After: 1` döner. Worker hedef başına süreçte saniyede 5 (ani 2) gönderim yapar ve süreç başına en çok 16 eşzamanlı işi destekler. Birden çok süreçte ortak kota yoktur.
