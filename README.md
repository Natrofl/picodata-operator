# picodata-operator

Kubernetes-оператор для управления кластером [Picodata](https://picodata.io) — распределённой СУБД
на базе Tarantool. Оператор реализует reconcile-loop на основе CRD `PicoclusterDB`.

## Быстрый старт в minikube

### Требования

- [minikube](https://minikube.sigs.k8s.io/docs/start/) v1.32+
- kubectl v1.28+
- Go 1.24+
- make

### 1. Запустить minikube

```sh
minikube start
```

### 2. Установить CRD

```sh
make install
```

Проверить что CRD появился:

```sh
kubectl get crd picoclusterdbs.picodata.picodata.io
```

### 3. Создать namespace и секрет с паролем admin

```sh
kubectl create namespace picodata

kubectl create secret generic picodata-admin-secret \
  --namespace picodata \
  --from-literal=password=T0psecret
```

### 4. Запустить оператор локально

Оператор запускается на хосте и подключается к кластеру через `~/.kube/config`:

```sh
make run
```

Оставить терминал открытым (или запустить в фоне). Логи оператора идут в stdout.

### 5. Применить sample-кластер

В отдельном терминале:

```sh
kubectl apply -k config/samples/
```

Sample создаёт кластер из двух тиров:
- `arbiter` — 2 репликасета, RF=1 → 2 пода; участвует в raft, не хранит данные
- `default` — 2 репликасета, RF=2 → 4 пода; основное хранилище

### 6. Наблюдать за запуском

```sh
kubectl get pods -n picodata -w
```

Ожидаемый результат (все поды `1/1 Running`):

```
NAME                               READY   STATUS    RESTARTS   AGE
arbiter-picodata-sample-1-0        1/1     Running   0          2m
arbiter-picodata-sample-2-0        1/1     Running   0          2m
default-picodata-sample-1-0        1/1     Running   0          2m
default-picodata-sample-1-1        1/1     Running   0          2m
default-picodata-sample-2-0        1/1     Running   0          2m
default-picodata-sample-2-1        1/1     Running   0          2m
```

Имена подов: `{тир}-{кластер}-{репликасет}-{инстанс}`.
Первая цифра — номер репликасета в тире (1-based),
вторая — номер инстанса внутри репликасета (0-based).

Состояние CR:

```sh
kubectl get picoclusterdb -n picodata
```

### 7. Подключиться к кластеру

Пробросить PostgreSQL-порт:

```sh
kubectl port-forward -n picodata pod/default-picodata-sample-1-0 5432:5432
```

Подключиться через psql:

```sh
psql "host=localhost port=5432 user=admin password=T0psecret dbname=picodata sslmode=disable"
```

Или пробросить HTTP (Web UI + метрики):

```sh
kubectl port-forward -n picodata svc/default-picodata-sample 8081:8081
```

Эндпоинты:
- `http://localhost:8081` — Web UI
- `http://localhost:8081/metrics` — Prometheus-метрики
- `http://localhost:8081/api/v1/health/ready` — readiness (без авторизации)

### 8. Удалить кластер

```sh
kubectl delete -k config/samples/
```

Удаление CR удаляет все связанные ресурсы (StatefulSet, Service, ConfigMap) через
ownerReference. PVC удаляются вместе с подами.

---

## Разработка

```sh
# Сгенерировать manifests и deepcopy
make manifests generate

# Запустить линтер
make lint

# Запустить unit-тесты
make test
```

## Документация

- [ADR-001: CRD PicoclusterDB](docs/adr/2026-04-14-picoclusterdb-crd.md)

## License

Apache 2.0
