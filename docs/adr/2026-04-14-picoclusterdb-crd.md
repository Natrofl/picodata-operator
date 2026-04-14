# ADR-001: CRD PicoclusterDB

**Дата:** 2026-04-14
**Статус:** Принято

---

## Контекст

Picodata — распределённая СУБД на базе Tarantool с поддержкой тиров, репликации,
шардирования и raft-консенсуса. До появления оператора единственным способом
развернуть Picodata в Kubernetes был Helm-чарт с ручным управлением.

Helm-чарт имеет ряд ограничений:
- нет контроля над жизненным циклом (нет reconcile-loop)
- нет отслеживания состояния кластера через `status`
- конфигурация описывается через `values.yaml`, а не декларативный k8s-ресурс
- невозможно автоматически реагировать на сбои или изменение конфигурации

Цель — создать Kubernetes-оператор, который управляет кластером Picodata
декларативно через Custom Resource.

---

## Решение

Введён CRD `PicoclusterDB` в группе `picodata.picodata.io/v1`.

### Почему один CRD, а не несколько

Рассматривались варианты разбить конфигурацию на несколько ресурсов:
`PicoclusterDB` (кластер) + `PicoclusterTier` (тир) + `PicoclusterBackup` (бэкап).

Отклонено: на этапе MVP это создаёт лишнюю сложность без выгоды.
Один ресурс описывает полное желаемое состояние кластера — это проще
в использовании и удобнее для GitOps.

### Почему `v1`, а не `v1alpha1`

Версия API отражает зрелость контракта с пользователем, а не зрелость реализации.
Переход с `v1alpha1` на `v1` требует миграции всех CR у пользователей.
Проще сразу начать с `v1` и избежать этой миграции в будущем,
поскольку структура CRD спроектирована на основе готового Helm-чарта
и хорошо понятных требований.

### Структура Spec

#### `image`

Описывает Docker-образ Picodata. Вынесен на верхний уровень (не в тир),
так как все тиры используют один и тот же образ — версия Picodata
едина для всего кластера.

#### `adminPassword`

Пароль admin-пользователя передаётся через ссылку на Secret (`secretKeyRef`),
а не хранится в CR напрямую. Это соответствует стандартной практике k8s
и не допускает попадания секретов в git при хранении манифестов в репозитории.

#### `cluster`

Содержит параметры уровня всего кластера, которые одинаковы для всех тиров:
`defaultReplicationFactor`, `defaultBucketCount`, `shredding`.
Эти параметры попадают в секцию `cluster:` файла `config.yaml` каждого инстанса.

#### `tiers`

Массив тиров — ключевая сущность. Каждый тир в Picodata — это отдельный класс
инстансов со своими характеристиками хранения, памяти и участия в raft.

**Один StatefulSet на репликасет, не на тир:**

Каждый тир состоит из `replicas` репликасетов, каждый репликасет — из
`replicationFactor` инстансов. Оператор создаёт отдельный StatefulSet для
каждого репликасета, а не один на весь тир.

Семантика поля `replicas` — количество репликасетов в тире.
Суммарное количество подов: `replicas × replicationFactor`.

Именование StatefulSet: `{tier}-{cluster}-{rsIndex}` (индекс 1-based).
Именование подов: `{tier}-{cluster}-{rsIndex}-{ordinal}`.

Пример для `replicas: 2, replicationFactor: 2`:
```
default-picodata-sample-1  →  default-picodata-sample-1-0, default-picodata-sample-1-1
default-picodata-sample-2  →  default-picodata-sample-2-0, default-picodata-sample-2-1
```

**Почему не один StatefulSet на тир (как в Helm-чарте):**

Helm-чарт использует `replicas` = суммарное количество подов и полагается
на то, что Picodata сама группирует поды в репликасеты по порядку join'а.
Это недетерминировано при параллельном старте.

Оператор явно передаёт `PICODATA_REPLICASET_NAME={tier}_{rsIndex}` каждому
поду, заставляя governor'а поместить под в нужный репликасет независимо от
порядка старта. Топология детерминирована и соответствует spec CR.

Это стандартная практика для операторов stateful-баз данных
(TiKV, CockroachDB, OpenSearch).

**Immutable поля тира** (`name`, `replicationFactor`, `canVote`):

Picodata фиксирует принадлежность инстанса тиру и репликасету в момент
первого вызова `box.cfg()`. После bootstrap изменить эти параметры
без полного сброса данных (rebootstrap) невозможно.
Поля помечены как immutable на уровне документации; в будущем
планируется добавить validating webhook для явного отклонения таких изменений.

#### `service`

Описывает три порта Picodata: `binaryPort` (iproto, 3301), `httpPort`
(Web UI + Prometheus metrics, 8081), `pgPort` (PostgreSQL-протокол, 5432).
Вынесен на верхний уровень, так как порты одинаковы для всех тиров.

#### Пробы

`startupProbe`, `livenessProbe`, `readinessProbe` задаются один раз на уровне
кластера и применяются ко всем тирам. По умолчанию используются HTTP-эндпоинты
Picodata (см. [ADR Health Check API](https://github.com/picodata/picodata/blob/master/doc/adr/2026-01-27-health-check-api.md)):

| Проба          | Эндпоинт                     | Успех / Неуспех        |
|----------------|------------------------------|------------------------|
| startupProbe   | `GET /api/v1/health/startup` | 200 / 503              |
| livenessProbe  | `GET /api/v1/health/live`    | всегда 200             |
| readinessProbe | `GET /api/v1/health/ready`   | 200 / 503              |

Все три эндпоинта не требуют авторизации (см. ADR Picodata).
Порт — `httpPort` (по умолчанию 8081).

Пользователь может переопределить любую пробу через поля
`spec.startupProbe`, `spec.livenessProbe`, `spec.readinessProbe`.

### Status

Оператор записывает в `status`:
- `phase` — агрегированное состояние: `Pending`, `Initializing`, `Ready`, `Degraded`
- `tiers[]` — `readyReplicas` и `desiredReplicas` для каждого тира
- `conditions[]` — стандартный k8s-формат условий с `lastTransitionTime`

`phase: Ready` выставляется когда `readyReplicas >= desiredReplicas` для всех тиров.

### Ресурсы, создаваемые оператором

На каждый тир оператор создаёт следующие ресурсы (в порядке создания):

1. **ConfigMap** (`{tier}-{cluster}`) — один на тир, генерирует `config.yaml`.
   Создаётся первым, так как StatefulSet-ы ссылаются на него через аннотацию
   `checksum/config`. Изменение конфига автоматически инициирует rolling restart.

2. **Service headless** (`{tier}-{cluster}-interconnect`) — один на тир,
   охватывает все поды тира.
   Обеспечивает стабильные DNS-имена вида
   `<pod>.<tier>-<cluster>-interconnect.<ns>.svc.cluster.local`.
   Используется для `iproto.advertise` и `peer` (bootstrap entry point).
   `publishNotReadyAddresses: true` — поды находят друг друга до Ready.

3. **Service ClusterIP** (`{tier}-{cluster}`) — один на тир,
   клиентский доступ к бинарному, http и pg портам.

4. **StatefulSet × replicas** (`{tier}-{cluster}-{rsIndex}`) — по одному на
   каждый репликасет тира. Каждый StatefulSet управляет `replicationFactor`
   подами. Политика `Parallel` — все поды запускаются одновременно:
   при `OrderedReady` с RF>1 pod-0 не может пройти startup probe (replicaset
   not-ready), пока pod-1 не запущен, а pod-1 не запустится пока pod-0 не Ready
   — deadlock. Параллельный старт безопасен — Picodata находит соседей через
   headless service.

   При уменьшении `tier.replicas` оператор удаляет StatefulSet-ы с индексом
   выше нового значения.

Все ресурсы получают `ownerReference` на `PicoclusterDB` — при удалении CR
все дочерние ресурсы удаляются автоматически через Garbage Collection.

### Конфигурация Picodata (новый стиль)

Оператор генерирует конфиг в новом формате (Picodata 26.x+):

```yaml
instance:
  iproto:
    listen: 0.0.0.0:3301
  http:
    listen: 0.0.0.0:8081
  pgproto:
    enabled: true
    listen: 0.0.0.0:5432
    tls:
      enabled: false
```

Вместо устаревшего формата (`pg.listen`, `http_listen`, `listen`).
`iproto.advertise` и `pgproto.advertise` передаются через env-переменные
`PICODATA_IPROTO_ADVERTISE` и `PICODATA_PG_ADVERTISE` — это позволяет
задать pod-специфичный FQDN без создания отдельного ConfigMap на каждый под.

---

## Рассмотренные альтернативы

### Использовать только Helm-чарт

Отклонено: нет reconcile-loop, нет автоматического восстановления,
нет нативного статуса в k8s.

### Operator SDK вместо kubebuilder

Operator SDK в текущей версии надстраивает kubebuilder.
Выбран kubebuilder напрямую — меньше абстракций, лучше контроль.

### Один StatefulSet для всех тиров

Отклонено: разные тиры имеют разные `resources`, `affinity`, `storageClassName`.
Один StatefulSet не может описать неоднородный pod template.

### Один StatefulSet на тир (как в Helm-чарте)

Рассматривался. Отклонён: `replicas` в этом случае означает суммарное количество
подов, а распределение по репликасетам определяется Picodata автоматически по
порядку join'а. Это недетерминировано при параллельном старте.
Текущий подход (один StatefulSet на репликасет + явный `PICODATA_REPLICASET_NAME`)
гарантирует соответствие топологии декларации в CR.

---

## Последствия

- Пользователь описывает весь кластер Picodata в одном YAML-файле
- Оператор идемпотентно приводит кластер к желаемому состоянию
- Удаление CR удаляет весь кластер включая PVC (через ownerReference + GC)
- Изменение `config.yaml` (через CR) автоматически инициирует rolling restart
  за счёт аннотации `checksum/config` на pod template

### Известные ограничения

- Immutable поля тира не валидируются webhook — пользователь может случайно
  изменить их; оператор проигнорирует изменение, но не сообщит об этом явно
- Масштабирование реализовано через изменение `replicas` (количества репликасетов)
  в CR; уменьшение может привести к потере данных при RF=1 и требует предварительного
  expel инстансов через Picodata API
- Backup и rebootstrap не реализованы (запланированы в следующих фазах)
