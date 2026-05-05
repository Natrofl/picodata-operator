# picodata-operator — установка

## Совместимость

| Компонент  | Версия |
|------------|--------|
| Picodata   | 26.1   |
| Kubernetes | 1.30+  |

## Требования

- kubectl с доступом к кластеру
- Права на создание CRD и ClusterRole

## Установка

### 1. Установить CRD

```sh
kubectl apply -f crd.yaml
```

### 2. Развернуть оператор

```sh
kubectl apply -f operator.yaml
```

Проверить, что оператор запустился:

```sh
kubectl get pods -n picodata-operator-system
kubectl logs -n picodata-operator-system deploy/picodata-operator-controller-manager -f
```

### 3. Создать namespace и секрет с паролем admin

```sh
kubectl create namespace picodata

kubectl create secret generic picodata-admin-secret \
  --namespace picodata \
  --from-literal=password=<пароль>
```

### 4. Применить CR

Создайте файл `cluster.yaml` по образцу и примените:

```sh
kubectl apply -f cluster.yaml
```

Следить за статусом:

```sh
kubectl get picoclusterdb -n picodata
kubectl get pods -n picodata -w
```

---

## Образы из приватного реестра

Если реестр требует аутентификации, нужно создать два pull secret — отдельно для оператора и для Picodata.

**Secret для оператора** (namespace `picodata-operator-system`):

```sh
kubectl create secret docker-registry regcred \
  --docker-server=docker-public.binary.picodata.io \
  --docker-username=<логин> \
  --docker-password=<пароль> \
  -n picodata-operator-system
```

Добавьте его в `operator.yaml` в `spec.template.spec`:

```yaml
imagePullSecrets:
  - name: regcred
```

**Secret для Picodata** (namespace где живёт кластер, например `picodata`):

```sh
kubectl create secret docker-registry regcred \
  --docker-server=docker-public.binary.picodata.io \
  --docker-username=<логин> \
  --docker-password=<пароль> \
  -n picodata
```

Укажите его в CR:

```yaml
spec:
  imagePullSecrets:
    - name: regcred
```

Оператор использует этот же secret при запуске init-контейнера (`config-init`), который работает на том же образе Picodata.

---

## Security context

Picodata внутри контейнера работает от пользователя `picodata` (UID 1000, GID 1000). Директория данных монтируется из PVC, который по умолчанию создаётся с правами `root:root 755` — процесс не может в него писать.

Чтобы Kubernetes автоматически выставил правильные права на том при монтировании, укажите `fsGroup: 1000` в `securityContext` каждого тира:

```yaml
tiers:
  - name: arbiter
    securityContext:
      fsGroup: 1000
    ...
  - name: default
    securityContext:
      fsGroup: 1000
    ...
```

Kubernetes при старте пода рекурсивно меняет GID всех файлов в PVC на 1000 и устанавливает setgid-бит на директории, после чего Picodata может читать и писать свои данные (снапшоты, xlog, admin.sock).

Если `securityContext` не указан, оператор подставляет `fsGroup: 1000` автоматически. Явное указание имеет смысл, если вы хотите добавить дополнительные параметры (`runAsUser`, `runAsNonRoot` и т.д.) или переопределить GID.

---

## Удаление

```sh
kubectl delete -f operator.yaml
kubectl delete -f crd.yaml
```
