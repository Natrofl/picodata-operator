# Picodata Kubernetes Operator — план разработки

## Что такое Picodata и зачем оператор

Picodata — распределённая СУБД на базе Tarantool с поддержкой тиров (tier),
репликации, шардирования и raft-консенсуса. Текущий способ деплоя в k8s — Helm-чарт.

Оператор даёт декларативный API (`PicoclusterDB` CRD) и управляет жизненным
циклом кластера автоматически: создаёт StatefulSets, Services, ConfigMaps,
следит за здоровьем, обновляет конфигурацию.

---

## Архитектурные особенности Picodata, важные для оператора

| Факт | Следствие для оператора |
|---|---|
| Один тир = один StatefulSet | На каждый `tier` в spec создаём отдельный StatefulSet |
| `peer` (initial_peers) нужен до старта | ConfigMap должен быть создан до Pod-а; peer = DNS-адрес pod-0 первого тира |
| При первом старте происходит rebootstrap (wipe + exec) | StatefulSet должен иметь PVC для сохранения данных между рестартами |
| `iproto_advertise` должен содержать FQDN-адрес пода в headless service | На каждый тир создаём headless Service; FQDN прописываем через env |
| Конфиг пишется в `config.yaml`, путь передаётся через `PICODATA_CONFIG_FILE` | ConfigMap монтируем как файл |
| Порты: 3301 (iproto), 8081 (HTTP/metrics), 5432 (pg) | Два Service на тир: headless (interconnect) + ClusterIP (клиентский) |
| Параметры тира immutable: name, replication_factor, can_vote | Оператор должен блокировать изменение этих полей через webhook или логику reconciler |
| Prometheus-метрики на `/metrics` (порт 8081) | Добавляем аннотации `prometheus.io/scrape` к Pod-ам |

---

## CRD: `PicoclusterDB`

```yaml
apiVersion: picodata.io/v1alpha1
kind: PicoclusterDB
metadata:
  name: my-cluster
  namespace: picodata
spec:
  # Docker-образ Picodata
  image:
    repository: docker.binary.picodata.io
    tag: "picodata:master"
    pullPolicy: IfNotPresent
  imagePullSecrets: []

  # Имя кластера (immutable после создания)
  clusterName: my-cluster

  # Пароль admin-пользователя — берём из Secret
  adminPassword:
    secretRef:
      name: picodata-admin-secret
      key: password

  # Параметры уровня кластера
  cluster:
    defaultReplicationFactor: 1
    defaultBucketCount: 3000
    shredding: false

  # Определение тиров — один или несколько
  tiers:
    - name: default
      replicas: 2                  # количество Pod-ов (инстансов)
      replicationFactor: 1         # immutable
      canVote: true                # immutable

      storage:
        size: 1Gi
        storageClassName: ""       # "" = default StorageClass

      memtx:
        memory: "128M"
      vinyl:
        memory: "64M"
        cache: "32M"

      pg:
        enabled: true              # слушать pg-порт снаружи Pod-а
        ssl: false

      log:
        level: info
        format: plain
        destination: null          # null = stdout

      resources:
        requests:
          cpu: "100m"
          memory: "128Mi"
        limits:
          cpu: "200m"
          memory: "256Mi"

      # Стандартные k8s-поля планирования
      affinity: {}
      tolerations: []
      nodeSelector: {}
      topologySpreadConstraints: []

      # Дополнительные env-переменные в Pod
      env: []

  # Настройки Service (одинаковы для всех тиров)
  service:
    type: ClusterIP
    ports:
      binary: 3301
      http: 8081
      pg: 5432

  # Пробы (применяются ко всем тирам)
  startupProbe:
    tcpSocket:
      port: binary
    periodSeconds: 30
    failureThreshold: 20
    timeoutSeconds: 3
  livenessProbe:
    tcpSocket:
      port: binary
    periodSeconds: 20
    failureThreshold: 3
    timeoutSeconds: 3
  readinessProbe:
    tcpSocket:
      port: binary
    periodSeconds: 20
    failureThreshold: 3
    timeoutSeconds: 3
```

### Status

```yaml
status:
  phase: Pending | Initializing | Ready | Degraded | Unknown

  tiers:
    - name: default
      readyReplicas: 2
      desiredReplicas: 2

  conditions:
    - type: Ready
      status: "True"
      reason: AllTiersReady
      message: "All 1 tier(s) are ready"
      lastTransitionTime: "..."
    - type: TierReady/default
      status: "True"
      ...
```

---

## Ресурсы k8s, создаваемые на каждый тир

| Ресурс | Имя | Назначение |
|---|---|---|
| `ConfigMap` | `{tier}-{cluster}` | `config.yaml` для инстансов тира |
| `Service` (Headless) | `{tier}-{cluster}-interconnect` | DNS для Pod-ов (iproto peer, advertise) |
| `Service` (ClusterIP) | `{tier}-{cluster}` | Доступ клиентов к порту pg/http |
| `StatefulSet` | `{tier}-{cluster}` | Сами Pod-ы с Picodata |

Плюс один раз на весь кластер:
- `ServiceAccount` — `{cluster}`

---

## Переменные окружения в Pod (аналог Helm-чарта)

```
POD_IP                 → status.podIP
INSTANCE_NAME          → metadata.name
INSTANCE_NAMESPACE     → metadata.namespace
PICODATA_IPROTO_LISTEN → $(INSTANCE_NAME):{binary_port}
PICODATA_IPROTO_ADVERTISE → $(INSTANCE_NAME).{tier}-{cluster}-interconnect.{ns}.svc.cluster.local:{binary_port}
PICODATA_PG_ADVERTISE  → $(INSTANCE_NAME).{tier}-{cluster}-interconnect.{ns}.svc.cluster.local:{pg_port}
PICODATA_FAILURE_DOMAIN → HOST=$(INSTANCE_NAME)
PICODATA_CONFIG_FILE   → {instanceDir}/config.yaml
PICODATA_ADMIN_SOCK    → {instanceDir}/admin.sock
PICODATA_ADMIN_PASSWORD → (из Secret через secretKeyRef)
```

---

## Структура проекта (kubebuilder)

```
picodata-operator/
  api/
    v1alpha1/
      picoclusterdb_types.go      # Go-типы CRD
      groupversion_info.go
      zz_generated.deepcopy.go
  internal/
    controller/
      picoclusterdb_controller.go # основной reconciler
      configmap.go                # генерация ConfigMap
      statefulset.go              # генерация StatefulSet
      service.go                  # генерация Service (headless + ClusterIP)
      serviceaccount.go
      status.go                   # обновление status
  config/
    crd/                          # сгенерированные манифесты CRD
    rbac/                         # RBAC для оператора
    manager/                      # Deployment оператора
    samples/                      # примеры CR
  Dockerfile
  Makefile
  go.mod
```

---

## Логика reconciler (основной цикл)

```
Reconcile(PicoclusterDB):
  1. Fetch PicoclusterDB — если не найден, выходим (удалён)
  2. Для каждого tier в spec.tiers:
     a. Reconcile ServiceAccount
     b. Reconcile ConfigMap         ← сначала, т.к. Pod монтирует его
     c. Reconcile Headless Service  ← нужен для DNS до старта StatefulSet
     d. Reconcile ClusterIP Service
     e. Reconcile StatefulSet
  3. Aggregate статусы всех tier → обновить status.phase и status.conditions
  4. Если что-то изменилось — requeue
```

### Важные детали reconciler:

- **OwnerReference**: все создаваемые ресурсы получают `ownerRef` на `PicoclusterDB` → автоматическая очистка при удалении CR
- **Immutable поля**: при обнаружении изменения `tier.name`, `replicationFactor`, `canVote`, `clusterName` — записываем Warning event и не применяем изменение
- **ConfigMap hash**: аннотация `checksum/config` на Pod-шаблоне в StatefulSet — триггер rolling restart при изменении конфига
- **Merge strategy**: используем `controllerutil.CreateOrUpdate` для идемпотентного reconcile

---

## Фазы разработки

### Фаза 1 — MVP: базовый деплой (текущая)

- [x] Анализ документации Picodata
- [ ] Инициализация проекта kubebuilder (`kubebuilder init`, `kubebuilder create api`)
- [ ] Определение типов CRD в `picoclusterdb_types.go`
- [ ] Генерация CRD-манифестов и deepcopy (`make generate manifests`)
- [ ] Реализация reconciler:
  - [ ] ServiceAccount
  - [ ] ConfigMap (config.yaml для каждого тира)
  - [ ] Headless Service (interconnect)
  - [ ] ClusterIP Service
  - [ ] StatefulSet с PVC
  - [ ] Управление OwnerReference
- [ ] Обновление `status` (phase, tiers, conditions)
- [ ] RBAC-манифесты
- [ ] Dockerfile для оператора
- [ ] Kustomize-конфиг для деплоя оператора
- [ ] Sample CR + README с инструкцией быстрого старта
- [ ] E2E тест: поднять кластер из 1 тира с 2 репликами в kind/minikube

### Фаза 2 — Backup (позже)

- Новый CRD `PicoclusterDBBackup`
- Поддержка SQL-команды `BACKUP OPTION(TIMEOUT=...)` через job/exec
- S3-интеграция (CSI или прямой upload)
- Restore: `picodata restore --path <path>`
- Scheduled backup (CronJob)

### Фаза 3 — Rebootstrap & Rolling Updates (позже)

- Graceful rolling update при изменении image/config
- Обработка сценариев потери кворума
- Webhook для валидации immutable полей

---

## Команды для старта

```bash
# Инициализация проекта
go install sigs.k8s.io/kubebuilder/cmd/kubebuilder@latest
kubebuilder init --domain picodata.io --repo github.com/picodata/picodata-operator
kubebuilder create api --group picodata --version v1alpha1 --kind PicoclusterDB

# Генерация кода
make generate
make manifests

# Локальный запуск (против kind/minikube)
make install          # устанавливает CRD в кластер
make run              # запускает оператор локально

# Сборка и деплой образа оператора
make docker-build IMG=docker.binary.picodata.io/picodata-operator:dev
make deploy IMG=docker.binary.picodata.io/picodata-operator:dev
```

---

## Зависимости

- Go 1.22+
- kubebuilder v3
- controller-runtime v0.18+
- k8s.io/api, k8s.io/apimachinery, k8s.io/client-go (через controller-runtime)
