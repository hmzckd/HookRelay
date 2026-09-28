# Yerel Kubernetes (kind)

HR-025, aynı Docker image'ını tek node'lu yerel kind cluster'ında API, worker, migration Job ve iki demo alıcısı olarak çalıştırır. PostgreSQL 17 tek instance'dır. Bu kurulum eğitim/laboratuvar içindir; internete açık bir giriş noktası yoktur.

## Hazırlık ve başlatma

Windows PowerShell'de proje kökünde Docker Desktop'ın Linux motorunu açın. Compose veya yerel Go API'si `8080` portunu kullanıyorsa önce durdurun. Compose verisini silmeden durdurmak için `docker compose --env-file .env.compose down` kullanın; `-v` eklemeyin.

```powershell
if (-not (Test-Path .env.compose)) { .\scripts\init-compose-env.ps1 }
docker compose --env-file .env.compose build api
.\scripts\install-kind-tools.ps1
.\scripts\kind-up.ps1
.\scripts\smoke-kind.ps1
```

`install-kind-tools.ps1`, kind v0.33.0 ve Kubernetes v1.37.0 ile eşleşen kubectl'ı `tmp/tools/` altına indirir; resmî SHA-256 dosyalarıyla doğrular. Ağ erişimi gerekir. `kind-up.ps1`, yalnızca `kind-hookrelay` bağlamını ve `hookrelay` namespace'ini kullanır; kubeconfig `tmp/kind-kubeconfig` dosyasındadır. Önce PostgreSQL hazır olur, ardından ayrı migration Job tamamlanır; sonra uygulama Deployment'ları açılır. Aynı betik tekrar çalıştırılabilir. Her çalışmada migration Job yeniden oluşturulup checksum'lı migration'lar tekrar denetlenir.

`.env.compose` içindeki beş farklı rastgele değer Kubernetes Secret'a aktarılır. Betik kısa ömürlü `tmp/kind-secrets.env` dosyasını `finally` bloğunda siler. Bu dosyayı veya `kubectl get secret -o yaml` çıktısını paylaşmayın. Secret, cluster içinde varsayılan olarak kapsamlı bir secret yönetimi/şifreleme düzeni sağlamaz. ConfigMap yalnızca gizli olmayan ayarları taşır.

```powershell
.\tmp\tools\kubectl.exe --kubeconfig .\tmp\kind-kubeconfig -n hookrelay get pods,deployments,job,pvc
.\tmp\tools\kubectl.exe --kubeconfig .\tmp\kind-kubeconfig -n hookrelay logs job/hookrelay-migrate
```

Smoke betiği API Service'e yalnızca `127.0.0.1:8080` üzerinden geçici port-forward açar, iki hedefli örnek olay gönderir, iki teslimatın `succeeded` olmasını bekler ve port-forward'ı kapatır. Kullanıcı arayüzü veya dış ingress yoktur. Elle API çağrısı yapacaksanız ayrı terminalde şunu açık tutun:

```powershell
.\tmp\tools\kubectl.exe --kubeconfig .\tmp\kind-kubeconfig -n hookrelay port-forward service/api 8080:8080 --address 127.0.0.1
```

İşiniz bitince `Ctrl+C` ile yalnızca port-forward'ı kapatın. Cluster sonraki v1.0 doğrulaması için kalabilir. Tamamen kaldırmak isterseniz önce gerekli veriyi yedekleyin, sonra `kind delete cluster --name hookrelay --kubeconfig .\tmp\kind-kubeconfig` çalıştırın. kind node'u ve içindeki `standard` local-path volume birlikte silindiğinde PostgreSQL verisi de kaybolur; PVC, cluster dışı yedek değildir. Buna karşılık sıradan uygulama Pod'u yeniden oluşturulduğunda PVC yerinde kalır. Worker Pod kaybı ve API rollout deneyi [HR-026 kaydındadır](releases/v0.8-hr026.md).

Her Deployment'da CPU/bellek istekleri ve sınırları, startup/readiness/liveness kontrolleri bulunur. API ve alıcılar root olmayan, salt okunur dosya sistemiyle çalışır. PostgreSQL bu demoda tek Deployment ve tek PVC kullanır; yüksek erişilebilir veritabanı değildir. Demo profili yalnızca dahili `demo-a`/`demo-b` servislerine gider. Genel HTTPS hedefleri için uygulamanın mevcut SSRF denetimi korunur. Ağ eklentisiyle uygulanmış NetworkPolicy bu görevde kurulmadığı için ağ izolasyonu iddia edilmez.

[kind hızlı başlangıç](https://kind.sigs.k8s.io/docs/user/quick-start/), [Kubernetes PVC kavramı](https://kubernetes.io/docs/concepts/storage/persistent-volumes/) ve [probe davranışı](https://kubernetes.io/docs/concepts/workloads/pods/probes/) ayrıntı verir.

## HR-026: arıza ve rollout deneyi

Worker manifesti artık iki replika ister. Aynı kuyruğu paylaşmalarını görmek için önce worker'ları durdurup API üzerinden 12 olay biriktirin, sonra manifesti yeniden uygulayın:

```powershell
.\tmp\tools\kubectl.exe --kubeconfig .\tmp\kind-kubeconfig -n hookrelay scale deployment/worker --replicas=0
.\scripts\kind-enqueue.ps1 -Count 12
.\tmp\tools\kubectl.exe --kubeconfig .\tmp\kind-kubeconfig apply -f .\k8s\60-worker.yaml
.\tmp\tools\kubectl.exe --kubeconfig .\tmp\kind-kubeconfig -n hookrelay rollout status deployment/worker
```

Pod kaybı deneyi yalnızca yerel demo alıcısını değiştirir. İlk olay kimliğini `kind-enqueue.ps1` çıktısından alıp `-EventId` parametresine koyun. Zorla Pod silme anında sürecin gerçekten durduğuna dair eşzamanlı onay vermez; kalıcı girişim geçmişini ve son sonucu ayrıca kontrol edin. `slow` modunu iş bitince `ok` durumuna döndürün:

```powershell
.\tmp\tools\kubectl.exe --kubeconfig .\tmp\kind-kubeconfig -n hookrelay scale deployment/worker --replicas=0
.\scripts\set-kind-demo-a-mode.ps1 -Mode slow
.\scripts\kind-enqueue.ps1 -Count 4 -DemoAOnly
.\scripts\kind-kill-active-worker.ps1 -EventId ILK_OLAY_ID
.\scripts\set-kind-demo-a-mode.ps1 -Mode ok
.\tmp\tools\kubectl.exe --kubeconfig .\tmp\kind-kubeconfig -n hookrelay rollout status deployment/worker
```

API Service'in Pod yenilenirken verdiği `/readyz` yanıtlarını cluster içindeki demo-b Pod'undan ölçmek ve bozuk migration'ın yeni manifest uygulamasını durdurduğunu denemek için:

```powershell
.\scripts\verify-kind-api-rollout.ps1
.\scripts\verify-kind-migration-gate.ps1
.\scripts\smoke-kind.ps1
```

Migration hata betiği geçici başarısız Job oluşturur, API Deployment generation değerini kontrol eder ve normal Job'u geri yükler. Mevcut API süreçleri bu hata testinde çalışmaya devam eder; sınanan şey **yeni manifest uygulamasının durmasıdır**. Rollout betiği aynı image ile API Pod'unu yeniler ve örneklenen istekleri sayar; sıfır başarısız örnek mutlak sıfır kesinti garantisi değildir. [Ölçüm ve sınırlar](releases/v0.8-hr026.md).
