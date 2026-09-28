# Kısa yerel demo

Docker Desktop Linux motoru açık olmalı. Proje kökünde PowerShell kullanın. `.env.compose` yoksa bir kez oluşturun; dosyadaki değerleri paylaşmayın.

```powershell
if (-not (Test-Path .env.compose)) { .\scripts\init-compose-env.ps1 }
docker compose --env-file .env.compose up --build -d --wait
.\scripts\smoke-compose.ps1
.\scripts\demo-compose.ps1 -ProjectName hookrelay
```

`smoke-compose.ps1` bir olayı iki alıcıya gönderir ve iki teslimatın `succeeded` olmasını bekler. `demo-compose.ps1` demo-a alıcısını sırayla farklı modlarda yeniden açar. Şunları kontrol eder:

| Durum | Beklenen sonuç |
| --- | --- |
| Aynı üretici isteği tekrarlandı | Aynı event ID ve `202`; farklı içerikte `409` |
| Alıcı önce iki kez `500` döndü | Üçüncü girişimde `204`, teslimat `succeeded` |
| Alıcı kaydetti ama yanıt kayboldu | İki girişim, alıcı veritabanında tek yan etki |
| Alıcı sürekli `500` döndü | Beş girişimden sonra `dead` |
| İç ağ hedefi eklenmek istendi | `400`, hedef kaydedilmedi |

Betik gerçek örnek olaylar açar; mevcut veriyi silmez. Sonunda alıcıyı normal `ok` moduna döndürür. Çıktıda kimlikleri ve sayıları gösterir, anahtarları göstermez. Testten sonra `docker compose --env-file .env.compose down` ile servisleri kapatabilirsiniz; volume korunur. `down -v` veriyi siler.

Üç dakikalık video akışı: projeyi ve tek transaction sınırını 20 saniyede anlatın; Compose'u açıp iki başarılı teslimatı gösterin; demo betiğinin retry/yanıt kaybı/`dead` satırlarını ve girişim geçmişini gösterin; son olarak [CI](ci.md) sonucunu ve [teslimat sınırlarını](delivery-semantics.md) belirtin. Bu demo yerel alıcıları kullanır; dış HTTPS veya üretim kapasitesi göstermez.
