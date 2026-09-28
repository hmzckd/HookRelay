# CI ve yerel kontrol

GitHub Actions, her push ve pull request için Ubuntu üzerinde çalışır. PostgreSQL 17 servisinde geçici bir `hookrelay_test` veritabanı açar. Sırasıyla Go biçimini, `go vet` sonucunu, gerçek DB entegrasyon testlerini, race testini ve Docker image build'ini kontrol eder. Testler kendi şemalarını açıp kaldırır. İş akışı [ci.yml](../.github/workflows/ci.yml) dosyasındadır.

Windows'ta aynı yerel kontroller için önce ayrı test veritabanının URL'sini ayarlayın:

```powershell
$env:TEST_DATABASE_URL = 'postgres://kullanici:parola@127.0.0.1:5432/hookrelay_test?sslmode=disable'
.\scripts\check.ps1
```

Bu komut Docker Desktop çalışırken image'ı da oluşturur. Yalnızca Docker build'ini atlamak için `-SkipImage` kullanın; böyle bir koşu tam kontrol sayılmaz. Windows'ta C derleyicisi yoksa race testi atlanır ve komut bunu açıkça yazar. GitHub'ın Linux koşusu race testini çalıştırır. `TEST_DATABASE_URL` verilmezse betik hata verir; entegrasyon testleri sessizce atlanmaz.

Gerçek parola `.env` veya terminal dışında bir belgeye yazılmamalıdır. CI'daki `test-password` yalnızca geçici test container'ına aittir.
