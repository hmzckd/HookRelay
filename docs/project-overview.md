# HookRelay ne yapar?

Bir mağazada `order.created` olayı oluştuğunu düşünün. Depo sistemi ve bildirim sistemi bu olayı bilmek istiyor. Mağaza bu iki sistemin o anda açık olup olmadığını beklemek yerine olayı HookRelay API'sine gönderir. HookRelay olayı ve iki teslimat işini PostgreSQL'e **tek transaction** içinde kaydeder ve `202 Accepted` döndürür. Bu yanıt teslimatın bittiği anlamına gelmez; olay güvenle kuyruğa alınmıştır.

```text
Mağaza → HookRelay API → PostgreSQL'de olay + teslimat işleri
                              ↓
                       Go worker süreçleri
                         ↙          ↘
                    Depo alıcısı   Bildirim alıcısı
```

Worker'lar hazır işleri veritabanından çakışmadan sahiplenir, hedefe imzalı HTTP `POST` gönderir ve her girişimin sonucunu saklar. Geçici hatada sınırlı yeniden deneme vardır. Süreç sonuç yazmadan ölürse işin sahiplik süresi dolunca başka worker tekrar deneyebilir. Bu nedenle teslimat **en az bir kez** olabilir; uzak sistemin aynı `delivery_id` için yan etkiyi tekilleştirmesi gerekir. Demo alıcısı bunu PostgreSQL'de gösterir.

**Webhook**, bir olay olduğunda başka sisteme HTTP isteğiyle haber verme yöntemidir. Sürekli “yeni sipariş var mı?” diye sorgulamak yerine olay üretildiğinde bildirim gelir. HookRelay bu bildirimlerin gönderici tarafındaki güvenilir ara katmandır: kayıt, imza, tekrar deneme, geçmiş ve arıza toparlama sağlar. Örnekteki demo alıcıları gerçek depo/bildirim hizmetlerinin yerini tutar; gerçek müşteri verisi kullanılmaz.

**Go**, ağ istekleri ve eşzamanlı worker kodu için iyi bir öğrenme alanı olduğu ve derlenmiş tek çalıştırılabilir dosyalarla container kurulumunu sade tuttuğu için seçildi. PostgreSQL, olay ve işlerin birlikte kalıcı kaydını, transaction'ları ve birden çok worker'ın `SKIP LOCKED` ile aynı işi üstlenmemesini sağlar. Bu seçim `.NET` ile yapılamayacağı anlamına gelmez; bu portföy projesi mevcut `.NET` işlerinden farklı olarak Go, eşzamanlılık ve arıza yönetimi becerisini gösterir.

**Docker** uygulamayı ve bağımlılıklarını container image'ına paketler. **Docker Compose** aynı bilgisayarda API, worker, PostgreSQL ve demo alıcılarını birlikte başlatır. **Kubernetes** bu container'ların istenen sayıda çalışmasını, hazır olmayanların trafikten çıkarılmasını ve silinen Pod'un yerine yenisinin açılmasını yönetir. Bu projede `kind`, Kubernetes'i yerel Docker üzerinde çalıştırır; bulut hesabı veya EKS kullanılmaz.

Kubernetes terimleri bu projedeki karşılıklarıyla:

| Terim | HookRelay'deki karşılığı |
| --- | --- |
| Pod | Bir API, worker veya alıcı süreç örneği |
| Deployment | API ve worker Pod sayısını ve güncellemesini yöneten tanım |
| Service | API, PostgreSQL ve alıcılar için sabit cluster içi adres |
| Job | Uygulamadan önce bitmesi gereken migration çalışması |
| PVC | Yerel PostgreSQL verisini Pod yenilenirken tutan volume isteği |
| ConfigMap / Secret | Gizli olmayan ayarlar / parola ve anahtarlar |
| Readiness / liveness | Trafik almaya hazır olma / süreç canlılığı kontrolü |

Kubernetes, uygulamanın transaction, idempotency veya alıcı tekilleştirme kurallarının yerini almaz. Yerel cluster tek node ve tek PostgreSQL instance kullanır; cluster silinirse yerel veri de kaybolur. Bu kurulum üretim yüksek erişilebilirliği veya bulut deneyimi iddiası değildir. [Ayrıntılı kurulum](kind.md), [teslimat sınırları](delivery-semantics.md), [yol haritası](../PLAN.md).
