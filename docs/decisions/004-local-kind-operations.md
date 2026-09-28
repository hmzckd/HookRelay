# ADR 004 — Yerel kind ile Kubernetes işletim deneyi

Durum: v0.8'de kabul edildi; yalnızca yerel laboratuvar kurulumu.

Sorun: Compose tek makinede hizmetleri çalıştırır, fakat istenen Pod sayısı, Service yönlendirmesi, readiness, rollout ve Pod kaybı davranışını Kubernetes üzerinde göstermek gerekir. Bu öğrenme hedefi için ücretli bulut veya EKS zorunlu değildir.

Karar: `kind` üzerinde tek node'lu cluster kurulur. Aynı root olmayan `hookrelay:local` image'ı API, iki worker, migration Job ve demo alıcılarına verilir. PostgreSQL 17 tek Deployment ve 1 GiB PVC kullanır. ConfigMap gizli olmayan ayarları, çalışma zamanında oluşturulan Secret yerel anahtarları taşır. API yalnızca cluster içi Service ve host loopback port-forward ile erişilir. Migration Job tamamlanmadan yeni uygulama manifestleri uygulanmaz.

Gerekçe: HR-025 kurulumu ve HR-026'daki iki worker, Pod kaybı, API rollout ve bozuk migration kapısı gerçek yerel cluster'da tekrarlandı. API rollout izlemelerinde ilk geçici hata görüldü; 5 saniyelik `preStop` drenajı sonrası 72 cluster içi örneğin tamamı geçti.

Bedel: Tek node ve local-path PVC, node/cluster silinmesine karşı veri korumaz. PostgreSQL yüksek erişilebilir değildir. Kubernetes Secret tam secret yönetimi, mevcut ağ eklentisinde NetworkPolicy enforcement veya sıfır kesinti garantisi sayılmaz. [Kurulum ve deney](../kind.md), [ölçüm kaydı](../releases/v0.8-hr026.md).
