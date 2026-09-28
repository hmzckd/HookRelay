# HookRelay görev durumu

Planın tamamı: [PLAN.md](../PLAN.md). Durumlar `TODO`, `IN_PROGRESS`, `VERIFY`, `DONE`, `BLOCKED`.

| Görev | Durum | Kanıt / kalan iş |
| --- | --- | --- |
| HR-001 Araç ve klasör kontrolü | DONE | Go 1.27.1 ve PostgreSQL 17.11 taşınabilir test araçları proje klasörü dışındaki `tmp/` altında; proje içinde yalnızca kaynak ve belgeler. |
| HR-002 İlk HTTP/olay sözleşmesi | DONE | `api/openapi.yaml`, `docs/architecture.md`, örnek olay. |
| HR-003 Veri modeli ve karar | DONE | `migrations/`, ADR 001; tek transaction kararı. |
| HR-004 Go modülü ve API yaşam döngüsü | DONE | `go test`, `go vet`; gerçek API süreci başladı, kapatılıp yeniden açıldı. |
| HR-005 Migration ve atomik kayıt | DONE | Migration CLI temiz PostgreSQL'e uygulandı ve ikinci kez sorunsuz çalıştı; ikinci teslimat hatası transaction'ı tamamen geri aldı. |
| HR-006 Kabul/okuma ve sınırlar | DONE | HTTP testleri; gerçek süreçte 401/202/200, iki pending iş ve yeniden başlatma sonrası okuma doğrulandı. |
| HR-007 Tek worker ve girişim geçmişi | DONE | PostgreSQL claim/sonuç işlemleri, tek worker kilidi ve API sorguları gerçek DB'de doğrulandı. |
| HR-008 İmza ve demo alıcısı | DONE | HMAC test vektörü, geçerli ve bozulmuş imza; iki gerçek demo alıcısında `204`, imzasız istekte `401`. |
| HR-009 Ağ sınırları | DONE | Tam localhost hedef listesi, proxy/redirect kapalı, toplam timeout ve yanıt sınırı; `500` ve yavaş alıcı demosu. |
| HR-010 Üretici isteği idempotency'si | DONE | Zorunlu anahtar, tek transaction'da olay/iş/anahtar kaydı, eşzamanlı 20 istekten tek olay, farklı içerikte `409`; gerçek PostgreSQL testi. |
| HR-011 Hata sınıflandırma ve retry politikası | DONE | [Politika ve hata tablosu](retry-policy.md), sahte saat/jitter ile karar testleri, `Retry-After` sınırı; HR-012 ile worker'a bağlandı. |
| HR-012 Kalıcı zamanlama ve terminal durumlar | DONE | `retry_wait`/`dead`, veritabanı saati, yeniden bağlantı testi ve gerçek süreçlerde `500, 500, 204` demosu. |
| HR-013 Atomik çok worker sahiplenmesi | DONE | `SKIP LOCKED`, iş başına token ve lease; iki ayrı DB bağlantısıyla 100 işi çakışmadan sahiplenme, kilitli satırı atlama ve yanlış token sonucunu reddetme testleri. |
| HR-014 Lease kurtarma ve eski sonuç reddi | DONE | Süresi dolmuş `processing` işi `unknown` girişimle kurtarma; eski token/lease sonucunu reddetme; bütçe, transaction rollback ve üç ayrı süreç çökmesi senaryosu gerçek PostgreSQL'de doğrulandı. |
| HR-015 Sınırlı worker havuzu ve kapanış | DONE | Süreç başına 1–16 arası kapasite, boş yer kadar claim, 10 saniyelik kapanış penceresi ve zorlanan iptalde lease kurtarması; gerçek PostgreSQL'de 12 iş/3 kapasite testi. Race detector bu Windows ortamında çalıştırılamadı. |
| HR-016 Alıcıda teslimat tekilleştirmesi | DONE | PostgreSQL benzersiz `delivery_id` kaydı; 20 eşzamanlı tekrar ve alıcı yeniden başlatması tek yan etki; gerçek imzalı HTTP'de ilk yanıt kaybı, ikinci girişimde `204`, toplam tek kayıt. |
| HR-017 Hedef yönetimi ve yetkiler | DONE | Ayrı `ADMIN_TOKEN`, hedef oluşturma/listeleme/okuma/güncelleme, sürüm çakışması, kapatma ve teslimat URL/anahtar snapshot'ı; gerçek PostgreSQL ve HTTP yetki testleri. [Ayrıntı](releases/v0.5-hr017.md). |
| HR-018 DNS/IP bağlantı politikası | DONE | Yalnızca HTTPS/443, URL sözdizimi, tüm DNS cevaplarının public IP denetimi ve sayısal IP'ye sabitlenmiş bağlantı; IPv4/IPv6, karışık DNS, rebinding ve redirect testleri. Demo istisnası artık yalnızca açık `demo` profilinde. [Tehdit modeli](target-security.md). |
| HR-019 Secret döndürme ve hız sınırları | DONE | Git dışı sürümlü anahtar dosyaları; eski/yeni teslimat ve alıcı doğrulama testleri; ayrı public/demo profili; log maskeleme testi; API ve worker süreç içi hız sınırları. [Anahtar rehberi](key-rotation.md), [geliştirme kaydı](releases/v0.5-hr019.md). |
| HR-020 Log ve metrikler | DONE | Korelasyonlu JSON logları ve yöneticiye özel Prometheus metrikleri; kuyruk, retry, sonuç ve süre ölçümleri gerçek PostgreSQL'de doğrulandı. [Rehber](observability.md), [geliştirme kaydı](releases/v0.6-hr020.md). |
| HR-021 Sağlık ve DB kesintisi | DONE | API ve worker canlılık/hazırlık ayrımı, worker ilerleme özeti, sınırlı yeniden bağlanma; test bağlantısındaki kesinti/geri dönüş gerçek PostgreSQL ile doğrulandı. [Rehber](health.md), [geliştirme kaydı](releases/v0.6-hr021.md). |
| HR-022 Saklama ve geri yükleme | DONE | 30 günlük sınırlı/atomik temizlik ve idempotency penceresi PostgreSQL'de doğrulandı; tam yedek ayrı temiz yerel DB'ye geri yüklendi. Ana DB'de silme yapılmadı. [Rehber](retention-backup.md), [geliştirme kaydı](releases/v0.6-hr022.md). |
| HR-023 Küçük yük deneyi | DONE | 40'ar işte 1 ve 4 gerçek worker süreci, ayrı test şemaları ve gerçek demo alıcısıyla ölçüldü; 40/40 başarılı, veri/ayar/sınırlar kaydedildi. [Rapor](load-test.md), [geliştirme kaydı](releases/v0.6-hr023.md). |
| HR-024 Docker ve Compose | DONE | Tek root olmayan image, ayrı migration işi, sağlıklı Compose başlangıcı, iki alıcıya teslimat, worker kaybından toparlanma ve volume kalıcılığı gerçek Docker'da doğrulandı. [Rehber](compose.md), [geliştirme kaydı](releases/v0.7-hr024.md). |
| HR-025 Yerel cluster ve manifestler | DONE | kind cluster, ayrı migration Job, 5 hazır Deployment, Bound PVC ve iki başarılı teslimat gerçek Kubernetes'te doğrulandı; tekrar kurulumda önceki kayıtlar kaldı. [Rehber](kind.md), [geliştirme kaydı](releases/v0.8-hr025.md). |
| HR-026 Replica, Pod kaybı ve rollout | DONE | 24 iş iki worker arasında 12/12 paylaşıldı; Pod kaybından dört iş `unknown` girişim sonrası tamamlandı. API rollout ilk ölçümde 1 hata, 5 s drain sonrası 72/72 hazır; başarısız migration yeni manifestleri engelledi. [Deney kaydı](releases/v0.8-hr026.md), [komutlar](kind.md). |
| HR-027 Son davranışın belgelere yansıtılması | DONE | README, OpenAPI, güncel mimari/teslimat sözleşmesi, ADR 001–004 ve işletim sınırları v0.8 ile eşleştirildi; YAML ve 38 Markdown dosyasının yerel bağlantıları doğrulandı. [Kayıt](releases/v1.0-hr027.md). |
| HR-028 CI ve yerel eşdeğer kontroller | DONE | Biçim, `go vet`, gerçek PostgreSQL testleri ve image build yerelde geçti. Windows'ta race testi atlandı; [Linux CI #1](https://github.com/hmzckd/HookRelay/actions/runs/36450942953) beş kontrolün tamamını geçti. [Komutlar](ci.md), [kayıt](releases/v1.0-hr028.md). |
| HR-029 Temiz kurulum, demo ve kabul raporu | DONE | Boş ayrı Compose volume'unda 18 migration, 2/2 teslimat; retry, yanıt kaybı, `dead`, replay/409 ve özel hedef reddi; yeniden başlatmada veri korundu. Mevcut kind'da 2/2 smoke geçti. [Demo](demo.md), [v1.0 raporu](releases/v1.0.md). |

v0.1–v0.5 yerel doğrulama kayıtları [releases/](releases/) altında; ayrıntılı retry kararı [retry-policy.md](retry-policy.md) dosyasında.
