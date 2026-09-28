# HR-023: küçük yerel yük deneyi

`./scripts/measure-load.ps1` komutu gerçek PostgreSQL ve gerçek demo alıcısıyla iki ayrı koşu yapar: önce **1**, sonra **4** worker süreci. Her sürecin eşzamanlılık ayarı `WORKER_CONCURRENCY=1` olur. Betik yalnızca loopback'teki `hookrelay_test` veritabanında her koşu için benzersiz bir şema açar, migration'ları uygular ve 40 olay/40 teslimat işini doğrudan veritabanına ekler. API kabulü ve onun hız sınırı bu ölçümün dışındadır. Hedef `demo-a` (`127.0.0.1:18080`), alıcı modu `ok`, gönderim gövdesi `{"load":true}` ve worker yoklama aralığı 500 ms'dir. Gerçek HTTP imzası, alıcı kayıtları, veritabanı claim işlemleri ve işin tamamlanması ölçüm yolundadır. İşler başlamadan hazırlanır; süre worker süreçleri başlatılırken başlayıp bütün teslimatlar `succeeded` olduğunda durur.

Betik her koşuda başarılı teslimat, girişim ve alıcı tekilleştirme kayıtlarını sayar; p50/p95 süreyi teslimat satırının oluşturulmasından girişimin bitmesine kadar hesaplar. Bu p50/p95 süresine işlerin worker açılmadan önce beklediği kısa hazırlık aralığı da girer. Alıcı hata verirse, worker erken çıkarsa veya üç dakika içinde tüm işler bitmezse deneyi başarısız sayar. Sonunda kendi worker/alıcı süreçlerini durdurur, yalnızca kendi test şemalarını ve geçici binary/log dosyalarını kaldırır. Ana `hookrelay` veritabanını değiştirmez. `tmp/go-build` derleme önbelleği kalır.

Windows PowerShell'de proje klasöründen çalıştırma:

```powershell
. .\scripts\load-env.ps1
.\scripts\measure-load.ps1
```

`hookrelay_test` veritabanı ve onda `CREATE SCHEMA` yetkisi gerekir. Aynı makinede `18080` portunu kullanan demo alıcısını önce durdurun. İsterseniz `-Events 40` (4–200) ve PostgreSQL araçları başka konumdaysa `-PgBin 'C:\Program Files\PostgreSQL\17\bin'` kullanın. Betik `.env` değerlerini veya DB parolasını çıktıya yazmaz. Veri/sır içeren test ortamında geçici işlem dosyaları bulunduğundan yalnızca güvenilen yerel makinede çalıştırın.

24 Eylül 2026, Windows/amd64, Go 1.27.1, PostgreSQL 17.11, Intel Core i7-13620H (16 mantıksal işlemci), tek yerel veritabanı ve alıcı üzerindeki bir denemenin sonuçları:

| Worker süreci | Başarılı / girişim / alıcı kaydı | Tamamlama | Teslimat/s | p50 / p95 uçtan uca süre |
| ---: | ---: | ---: | ---: | ---: |
| 1 | 40 / 40 / 40 | 7,916 s | 5,05 | 4,599 / 8,110 s |
| 4 | 40 / 40 / 40 | 2,058 s | 19,44 | 1,604 / 2,498 s |

Bu koşuda gözlenen tamamlanma süresi yaklaşık 3,85 kat kısaldı. Gönderim limiti süreç başına 5/s olduğundan dört sürecin toplam izin verilen hızı da daha yüksektir; sonuç yalnızca worker sayısının etkisini ayırmaz. Tek tekrar ve çok küçük yük nedeniyle bunlar kapasite garantisi, üretim kıyaslaması veya kalıcı ölçeklenme iddiası değildir. CPU/bellek kullanımı, farklı payload boyutları, gerçek ağ gecikmesi ve API kabulü ayrıca ölçülmedi.
