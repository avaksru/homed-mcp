# Отчёт Главного Агента — 2026-06-08 (обновлён 18:21)

## Итог

Агент-кодер завершил исправления. Все тесты зелёные, `go vet` чистый.

## Что сделано (Агент-кодер)

### 1. `mcp-server/internal/mqtt/client.go`
- Добавлено поле `retainedHooks []func(topic, payload)` под `mu`.
- Добавлен метод `OnRetained(hook)` — публичный API для in-process наблюдателей.
- В `onMessage` хуки дёргаются **снаружи** `c.mu` (снимок среза под локом).
- Фильтрация по `affectsAliasMap()` в `cmd/server/main.go` снимает 99% retain-шума (только `status/` и `device/` инвалидируют snapshot).

### 2. `mcp-server/cmd/server/main.go::buildEndpointAliasResolver`
**Полностью переписан.** Логика:
- **Канонический источник**: `status/<service>` retain с `{"devices":[{"id","name"}]}`.
- **Fallback** (cold start): `device/<service>/<X>` retain, группировка по `availabilityTopic` + `lastSeen` ±5s.
- **Удалена** payload-fingerprint группировка по `expose/<service>/<X>` (корень бага).
- **Кеш + ленивая инвалидация**: snapshot под `sync.RWMutex`, флаг `dirty`, пересчёт на первом lookup'е после retain'а на `status/` или `device/`.
- **Лог-спам подавлен**: `addAlias()` логирует только конфликт (один Debugf), а не каждый lookup.
- **Prime-time fix**: ключи мапы теперь — **полные** `<service>/<id>` (раньше были голые `id` → lookup `custom/A` возвращал пустую строку).

### 3. `mcp-server/cmd/server/main_test.go` (новый, 6 тестов)

| # | Тест | Покрывает |
|---|------|-----------|
| 1 | `TestResolver_StatusService_CanonicalSource` | **Главный кейс бага**: 5 BLE-устройств с одинаковым expose-payload → 5 раздельных пар |
| 2 | `TestResolver_StatusService_InvalidatesOnRetained` | Кеш обновляется при новом retain'е |
| 3 | `TestResolver_ColdStart_DeviceRetain` | Cold-start путь через `device/<svc>/<X>` |
| 4 | `TestResolver_NoAliasesForUnknown` | Не выдумывает алиасы |
| 5 | `TestResolver_IgnoresUnrelatedRetained` | High-frequency retain на `status/<device>` не инвалидирует snapshot |
| 6 | `TestAddAlias_Idempotent` | `addAlias` не перезаписывает конфликтующие маппинги |

## Результаты сборки

```
ok    github.com/u236/homed-mcp/cmd/server       (6/6 tests pass)
ok    github.com/u236/homed-mcp/internal/config
ok    github.com/u236/homed-mcp/internal/homedweb
ok    github.com/u236/homed-mcp/internal/mcp
ok    github.com/u236/homed-mcp/internal/mqtt
ok    github.com/u236/homed-mcp/internal/recorder
go vet ./...     — clean
go build ./...   — clean
```

## Затронутые файлы

| Файл | Изменение |
|------|-----------|
| `mcp-server/internal/mqtt/client.go` | + `OnRetained`, + `retainedHooks`, инвалидация в `onMessage` |
| `mcp-server/cmd/server/main.go` | Переписана `buildEndpointAliasResolver` + `computeAliasMap` + `addAlias` + `affectsAliasMap` + структуры `statusServiceDevice`/`deviceEndpoint` |
| `mcp-server/cmd/server/main_test.go` | **Новый** — 6 unit-тестов |

## Что осталось из отчёта Агента-логера

- [x] Resolver spam (п.1) — **исправлено**
- [x] Корень бага payload-fingerprint (п.2) — **исправлено**
- [x] Возврат к `status/<service>` (п.3) — **сделано**
- [x] Snapshot+compute race (п.4) — **исправлено** (lazy invalidation)
- [ ] Обогащение `homed_list_devices` полем `currentState` (п.5) — **отдельная задача** (не блокер)
- [ ] Автодефолт `series` в `query_recorder` (п.6) — **отдельная задача** (не блокер)

## Сборка + доставка

- [ ] Собрать `homed-mcp-armv7` и `homed-mcp-linux-amd64`
- [ ] Скопировать на роутер 192.168.0.14
- [ ] Перезапустить procd-юнит
- [ ] Smoke: `homed_list_devices` возвращает 5 раздельных BLE-устройств
- [ ] Smoke: лог не содержит `alias resolver: ...maps to both` больше 1 раза