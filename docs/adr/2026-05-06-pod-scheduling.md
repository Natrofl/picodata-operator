# ADR-003: Планирование подов и anti-affinity

**Дата:** 2026-05-06  
**Статус:** Принято

## Контекст

Picodata — распределённая СУБД с репликацией. Для обеспечения отказоустойчивости поды одного репликасета должны находиться на разных воркер-нодах. При потере ноды должна выживать минимум одна реплика каждого репликасета.

Дополнительно: арбитры (тир с `canVote: true`) должны быть распределены по разным нодам для сохранения raft-кворума при отказе.

## Проблема

StatefulSet не предоставляет встроенного механизма распределения подов по нодам. Kubernetes по умолчанию планирует поды там, где есть ресурсы, без учёта топологии кластера БД.

Нельзя задать единый `affinity` в spec тира для всех StatefulSet-ов: правило вида "не ставить рядом с подами своего репликасета" требует знания конкретного значения лейбла `picodata.io/replicaset`, которое различается у каждого StatefulSet.

## Решение

Оператор автоматически инжектирует `podAntiAffinity` при построении каждого StatefulSet в `buildStatefulSet()`:

```go
affinity = mergeReplicasetAntiAffinity(tier.Affinity, replicasetLabels(cluster, tier, rsIndex))
```

Правило использует `requiredDuringSchedulingIgnoredDuringExecution` с `labelSelector` по лейблам конкретного репликасета и `topologyKey: kubernetes.io/hostname`. Это гарантирует размещение каждой реплики на отдельной ноде.

Пользовательский `tier.Affinity` мержится с авто-инжектируемым правилом — оба работают одновременно.

## Флаг отключения

Для деплоя на кластере с одной нодой (тесты, разработка) предусмотрено поле:

```yaml
tiers:
  - name: default
    disableAutoAntiAffinity: true
```

При `true` авто-инжект пропускается, пользовательский `affinity` применяется как есть.

## Рекомендации по арбитрам

Арбитры (RF=1, несколько репликасетов) автоматического anti-affinity не получают — каждый репликасет содержит один под, и правило внутри него бессмысленно. Рекомендуется задать вручную:

```yaml
affinity:
  podAffinity:
    preferredDuringSchedulingIgnoredDuringExecution:
      - weight: 100
        podAffinityTerm:
          labelSelector:
            matchLabels:
              app.kubernetes.io/name: default-<cluster>
          topologyKey: kubernetes.io/hostname
  podAntiAffinity:
    preferredDuringSchedulingIgnoredDuringExecution:
      - weight: 100
        podAffinityTerm:
          labelSelector:
            matchLabels:
              app.kubernetes.io/name: arbiter-<cluster>
          topologyKey: kubernetes.io/hostname
```

`preferred` вместо `required` — арбитры стартуют до дефолт-подов и не должны зависать в Pending.

## Отклонённые варианты

**TopologySpreadConstraints** — отклонено. Равномерно распределяет поды по нодам, но не гарантирует что два пода одного репликасета окажутся на разных нодах без знания конкретного значения лейбла репликасета.

**Единый affinity в spec тира** — недостаточно. Требует хардкода значения лейбла репликасета, невозможно выразить "не ставить рядом с подом из моего же репликасета" статически.
