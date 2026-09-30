# HR-030 — İsteğe bağlı bulut denemesi kararı

30 Eylül 2026. Bu belge bir **plan**dır. HookRelay için AWS hesabında kaynak açılmadı; bulutta çalışma veya maliyet ölçümü yapılmadı. Yerel v1.0 kabulü [ayrı raporda](releases/v1.0.md) tamamlandı. Bulut denemesi için kullanıcı harcama sınırı ve ücretli kaynak açma kararı ayrıca gerekir.

## Neyi öğrenmek istiyoruz?

İlk bulut denemesi, uygulamayı bir AWS makinesinde kurup dar ağ erişimi, IAM, veri kalıcılığı, yeniden başlatma ve kaynak kaldırmayı görmeye yarar. Kubernetes davranışı zaten [yerel kind deneyinde](kind.md) gösterildi. EKS ancak yönetilen Kubernetes kontrol düzlemini özellikle öğrenmek istersek ayrı bir deney olur.

| Yol | Sağladığı deneyim | Ücretli kalemler ve karar |
| --- | --- | --- |
| Yerel Compose/kind | Webhook akışı, Pod kaybı ve rollout; mevcut kanıtlar tekrar kullanılabilir. | AWS ücreti yok. Bulut ağı ve IAM öğrenilmez. |
| Tek EC2 + Compose | AWS erişimi, güvenlik grubu, disk, maliyet ve kaldırma. Mevcut Compose akışına en yakın kısa deneme. | EC2, EBS, public IPv4 ve kullanıma bağlı ek kalemler. **İlk AWS denemesi için aday.** |
| ECS Fargate + RDS | Yönetilen container çalıştırma ve yönetilen PostgreSQL. | Fargate istenen CPU/bellek/depolamaya, RDS çalışan DB ve depolamaya göre ücretlenir; ağ, log ve olası load balancer da hesaba girer. Ayrı tasarım ve fiyat hesabı gerekir. [ECS fiyatı](https://aws.amazon.com/ecs/pricing/), [RDS PostgreSQL fiyatı](https://aws.amazon.com/rds/postgresql/pricing/). |
| EKS | AWS tarafından yönetilen Kubernetes kontrol düzlemi ve yerel kind ile karşılaştırma. | Standart destekli cluster **0,10 USD/saat**, genişletilmiş destek **0,60 USD/saat**; node, disk ve ağ ayrıca ücretlenir. 8 saat yalnızca standart cluster **0,80 USD** eder. Ayrı bütçe ve deney kararı gerekir. [EKS fiyatı](https://aws.amazon.com/eks/pricing/). |

## Örnek kısa EC2 denemesi

Bu, kullanıcının kabul ettiği bütçe değil; hesap yapılabilmesi için seçilen **örnek senaryo**. Bölge `us-east-1` (Virginia): AWS'nin açık T3 fiyat tablosu bu bölgeyi kullanıyor. Bölgenin Türkiye'ye uzaklığı demoda gecikme yaratabilir; bu deney performans ölçümü değildir. Süre 8 saat, aynı gün kapatma. Tek Linux `t3.medium` (2 vCPU, 4 GiB RAM), tek 20 GiB gp3 root disk ve tek public IPv4 varsayılır. Makine boyutu yeterli mi diye gerçek denemede bellek izlenir; yeterli değilse boyut ve bütçe yeniden hesaplanır. Kalıcı, herkese açık bir demo hedeflenmez.

| Kalem | Kaynak/fiyat varsayımı | 8 saat |
| --- | --- | ---: |
| EC2 `t3.medium` On-Demand Linux | `0,0418 USD/saat`; [AWS T3, us-east-1](https://aws.amazon.com/ec2/instance-types/t3/) | 0,3344 USD |
| 20 GiB gp3 | `0,08 USD/GiB-ay` **örnek birim fiyatı**; [AWS EBS hesap örneği](https://aws.amazon.com/ebs/pricing/). Seçilen bölgenin gerçek fiyatı kurulumdan önce Calculator'da tekrar doğrulanmalı. 720 saatlik ay varsayımı. | 0,0178 USD |
| 1 public IPv4 | `0,005 USD/saat`; [AWS VPC fiyatı](https://aws.amazon.com/vpc/pricing/) | 0,0400 USD |
| **Hesaplanan taban** | Yuvarlama sonrası | **yaklaşık 0,39 USD** |

Aynı kaynaklar 24 saat açık kalırsa hesaplanan taban yaklaşık **1,18 USD** olur. Bu tutarlar **fatura tavanı değildir**: aşırı T3 CPU kredisi, internet çıkışı, ek disk/snapshot, log saklama, vergi ve hesap koşullarına bağlı kalemler dahil edilmedi. Free Tier veya kredi düşülmedi. Özellikle [T3 CPU kredi davranışı](https://aws.amazon.com/ec2/instance-types/t3/) ve [EBS'nin ayrılana kadar ücretlenmesi](https://aws.amazon.com/ebs/pricing/) dikkate alınmalı. EC2 dışındaki hizmetler, NAT Gateway, ALB, RDS ve EKS bu örnek kuruluma eklenmez. Kurulumdan hemen önce seçilen hesap/bölge ve kaynaklarla [AWS Pricing Calculator](https://calculator.aws/) tahmini kaydedilir.

Önerilen çalışma sınırı: yalnızca tek 8 saatlik oturum, örnek **5 USD toplam harcama eşiği** ve önceden belirlenmiş kapatma saati. Bu eşik kullanıcı bütçesi yerine geçmez. Başlamadan önce hesapta başka kaynaklar olup olmadığı ve geçerli harcama sınırı netleştirilir. AWS Budgets uyarısı kurulabilir ama harcamayı anında kesmez; AWS, bildirimlerin gecikebildiğini belirtiyor. [AWS Budgets](https://docs.aws.amazon.com/cost-management/latest/userguide/budgets-managing-costs.html).

## Kurulacak kaynaklar ve erişim sınırı

Sonraki uygulama görevi açılırsa önce altyapı tanımı hazırlanır: tek EC2 instance, tek root EBS volume, yalnızca bu instance için bir security group ve gerekiyorsa dar izinli instance role. Var olan VPC/subnet kullanılabilirliği ve hesabın mevcut maliyeti önce incelenir. Proje etiketi (`Project=HookRelay`, `Experiment=short-ec2`) kaynak envanterinde kullanılır. Ayrı NAT Gateway, load balancer, RDS, EKS, DNS/domain veya ücretli sertifika kurulmaz.

Compose API'si yalnızca instance loopback'inde dinler. PostgreSQL ve demo alıcıları dışarı port açmaz. Kişisel erişim için SSH yalnızca kullanıcının güncel IP'sinden izinli olur ve API'ye SSH tüneliyle bağlanılır. Bu yol dışarıdan HTTPS endpoint sağlamaz; gerçek public webhook kabulü istenirse TLS, alan adı ve erişim tasarımı ayrıca yapılır. Anahtarlar Git'e, image'a, instance user-data'sına veya komut geçmişine yazılmaz; kısa ömürlü, git dışı dosyalardan sağlanır. Demo verisi sahte olur. Tek makinedeki PostgreSQL için yüksek erişilebilirlik veya otomatik yedek iddiası yoktur; gerekirse deneme sonunda `pg_dump` ile dışarı alınır ve ayrı geri yükleme testi yapılır.

Kurulum kanıtı: seçilen commit/image sürümü, migration tamamlanması, API/worker sağlık sonucu, iki alıcıya tek örnek olay, yeniden başlatma sonrası kayıtların korunması, dış port taramasında yalnızca amaçlanan erişim ve gerçek çalışma süresi. Bulut kurulumu gerçekleşmeden CV'de AWS tecrübesi olarak yazılmaz.

## Kaldırma kontrolü

1. Başlangıçta hesap, bölge, kaynak kimlikleri, etiketler, başlangıç ve planlanan bitiş saati kaydedilir. Kapatma için takvim hatırlatıcısı konur; alarm tek koruma değildir.
2. Deneme biter bitmez Compose durdurulur. Gereken tek kullanımlık kanıtlar gizli bilgi içermeden alınır. Altyapı tanımıyla stack silinir; EC2'nin **terminate** edildiği doğrulanır (yalnızca `stop` yeterli değildir).
3. Aynı bölgedeki EBS volume/snapshot, Elastic IP veya başka public IPv4, security group, varsa IAM role/policy, CloudWatch log grubu ve bütçe alarmı kontrol edilir. Deneme için ayrılmış ücretli artıkları kaldırılır; ortak/önceden var olan kaynaklara dokunulmaz.
4. Kaynak envanterindeki gerçek son durum ve daha sonra gecikmeli görülebilen fatura kaydı rapora eklenir. Harcama ve kaldırma **gerçekleşmeden** bu belgeyi uygulama kanıtı saymayız.

**Karar:** HR-030 planı tamamlandı. İlk ücretli uygulama için tek EC2 + Compose en küçük kapsamlı adaydır; bölge/fiyat yeniden doğrulaması, kullanıcının harcama sınırı ve ayrı ücretli kaynak açma kararı henüz yoktur. EKS ayrı tutulur.
