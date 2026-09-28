# HR-024: Docker ile yerel demo

Bu demo için Docker Desktop'ın **Linux container** motoru çalışmalıdır. Proje klasöründeki PowerShell terminalinde bir kez gizli yerel ayar dosyası oluşturun:

```powershell
.\scripts\init-compose-env.ps1
docker compose --env-file .env.compose up --build -d
docker compose --env-file .env.compose ps
.\scripts\smoke-compose.ps1
```

İlk komut `git` ve Docker build bağlamı dışında kalan `.env.compose` dosyasına birbirinden farklı rastgele DB parolası, API anahtarları ve demo imza anahtarları yazar. Mevcut dosyanın üzerine yazmaz. Gerçek değerleri terminale, belgeye veya Git'e kopyalamayın. Bu dosyayı koruyun: kalıcı DB volume'u oluşturulduktan sonra yeni parola üretmek mevcut DB parolasını değiştirmez. [`.env.compose.example`](../.env.compose.example) yalnızca alan adlarını gösterir; çalıştırılacak değerler içermez.

Compose tek bir uygulama image'ı derler. Aynı image API, worker, tek seferlik migration ve iki demo alıcısı için farklı komutlarla açılır; uygulama süreçleri root olmayan kullanıcıyla çalışır. PostgreSQL 17 kendi `pgdata` adlı volume'unu kullanır. Önce DB sağlık kontrolü geçer, sonra ayrı migration işi tamamlanır; API ve alıcılar bundan sonra, worker da alıcılar sağlıklı olduktan sonra açılır. API hostta yalnızca `127.0.0.1:8080` üzerinden görünür. PostgreSQL ve demo alıcılarının portları hosta yayımlanmaz. API `/readyz`, worker `/readyz`, alıcılar `/readyz` ile kontrol edilir. [Compose bağımlılık/sağlık koşullarının belgeleri](https://docs.docker.com/compose/how-tos/startup-order/).

`smoke-compose.ps1` üretici anahtarını dosyadan okuyup iki hedefli bir olay gönderir ve iki teslimatın da `succeeded` olmasını bekler. Olay kimliğini çıktı olarak verir. Tekrar çalıştırmak yeni bir olay açar. Yerel Go süreçleri `8080` portunu kullanıyorsa önce durdurun. Compose'un kendi PostgreSQL'i masaüstündeki yerel PostgreSQL veritabanından ayrıdır.

Retry, yanıt kaybı, terminal hata ve idempotency demosu için çalışan Compose projesinde `.\scripts\demo-compose.ps1 -ProjectName hookrelay` çalıştırın. Betik demo-a alıcısını geçici olarak farklı modlarda yeniden açar ve sonunda `ok` moduna döndürür. Tek bir modu elle seçmek isterseniz `HOOKRELAY_DEMO_A_MODE` ortam değişkeni `ok`, `flaky`, `error` veya `drop-after-commit` olabilir; varsayılan `ok` değeridir. [Kısa demo akışı](demo.md).

Eski olayları koruyarak uygulamayı yeniden başlatmak veya kapatmak için:

```powershell
docker compose --env-file .env.compose down
docker compose --env-file .env.compose up -d
```

`down` container ve ağı kaldırır; `pgdata` volume'unu korur. `down -v` volume'u ve içindeki geçmişi **siler**; bu komut normal yeniden başlatma için kullanılmaz. Sistemi durdurup daha sonra açmak için ilk satırı, tekrar kullanırken ikinci satırı çalıştırın. Kaynak `.env` ile `.env.compose` ayrı ayarlardır.

Worker kaybını denemek için `docker compose --env-file .env.compose kill -s SIGKILL worker`, ardından `docker compose --env-file .env.compose up -d worker` çalıştırabilirsiniz. Başlamış bir HTTP gönderiminin alıcıda yan etki oluşturup oluşturmadığı belirsiz olabilir; bu yüzden alıcı `delivery_id` ile kendi kalıcı tekilleştirmesini yapmalıdır. Ayrı Docker volume'u için yedek/geri yükleme otomatik kurulmamıştır; [saklama ve geri yükleme rehberi](retention-backup.md) veritabanı yedeğinin sınırlarını açıklar.

Bu yapılandırma yerel eğitim demosudur. Token ve parolalar image katmanlarına eklenmez, fakat çalışan container ayarlarında Docker yöneticisi tarafından görülebilir; üretim secret yönetimi sağlamaz. Dış HTTPS hedefleri için önceki sürümün anahtar dosyası/SSRF kuralları geçerlidir. `HOOKRELAY_CONTAINER_MODE=compose` yalnızca iki tam demo localhost adresini sabit `demo-a`/`demo-b` servislerine yönlendirir; diğer iç ağ adresleri yine engellenir.
