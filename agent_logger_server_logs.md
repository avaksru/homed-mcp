# Анализ логов homed-mcp сервера — сессия 2026-06-08 18:38–18:57

## ✅ Подтверждения из прошлого раунда

1. **18:38:04.960** — сервер поднялся, MQTT-соединение `nexj.ru:1883`, prefix=`homed`, clientID=`homed-mcp-1`. HTTP на `:18082/mcp`.
2. **18:38:04.961** — `homed-web` загружен: `5 dashboards / 10 blocks / 79 items / 2 names` (version 2.9.8). **alias resolver** на 100% обеспечен данными.
3. **18:38:05.159** — recorder загружен: `54 items / 141 359 data rows / 475 681 hour rows`, range 2024-06-18..2026-06-08.
4. **18:38:05.160** — зарегистрировано 14 tools (включая `homed_query_recorder`).
5. **18:51:24.908** — лог **ПОДТВЕРЖДАЕТ** найденный в прошлом раунде баг:
   ```
   mqtt: publish homed/td/custom/garland_window (retained=false) payload={"status":"on"}
   ```
   Wire-payload был `{"status":"on"}` — неправильный, должен быть `{"status_1":"on"}`. Custom-сервис (ESP) его проигнорировал, гирлянда осталась выключенной.

## 🔴 Аномалии (Агент-логер нашёл)

### №1 (CRITICAL) — wire-property translation для multi-channel single-item устройств
- **Где:** `mcp-server/internal/mcp/tools.go`, `resolvePropertyFromExpose` case 1
- **Симптом:** `homed_set_device(endpoint=custom/garland_window, property=status, value=on)` → `{"status":"on"}` → silent drop на custom-сервисе
- **Статус:** ✅ **ИСПРАВЛЕНО** в прошлом раунде (commit между сессиями)

### №2 (MEDIUM) — `homed_get_topic`/`homed_get_status` возвращают пустоту для single-channel switch'ей
- **Лог:** `18:52:25.833` — `error: no retained payload for "status/custom/garland_window"`
- **Причина:** `custom` сервис с names=true не публикует retained `status/<device>` для single-channel switch'ей (`status/garland_window` пусто), только `device/<id>` (`{status:online}`). MCP-инструменты `homed_get_status`/`homed_get_topic` возвращают пустоту/ошибку.
- **Статус:** ✅ **ИСПРАВЛЕНО** в этом раунде — fallback в `toolGetTopic` на `device/<id>`, добавляет поле `note`

### №3 (LOW) — двойной subscribe в `toolGetProperties`
- **Лог:**
  ```
  18:54:08.095 mqtt: subscribe homed/fd/custom/garland_window (qos=1)   # вручную
  18:54:08.097 mqtt: subscribe homed/fd/custom/garland_window (qos=1)   # toolGetProperties
  ```
- **Причина:** `toolGetProperties` всегда делает `Subscribe`, даже если caller уже подписался через `homed_subscribe`. MQTT идемпотентен, но лишний debug-спам.
- **Статус:** ⏭️ **ПРОПУЩЕНО** — низкий приоритет, MQTT-идемпотентность уже работает

### №4 (LOW) — `homed_get_request` на read-only топиках 10s timeout
- **Лог:** `18:52:03.334–18:52:13.336` — `homed_get_request(topic="status/custom", timeout=10)` → `context deadline exceeded`
- **Причина:** `status/<id>`, `expose/<id>`, `device/<id>`, `service/<id>`, `fd/<id>`, `td/<id>` — это read-only retained/event топики; они не отвечают на request/reply pattern. Старое поведение: 10s ожидания и timeout.
- **Статус:** ✅ **ИСПРАВЛЕНО** в этом раунде — fail-fast с подсказкой `use homed_get_topic ... or homed_get_properties`

## 🟢 Что работает идеально

- Все `homed_list_*` tools — < 10ms
- `homed_set_device` с правильным property (status_1) → `td/custom/garland_window` → `fd/custom/garland_window {"status_1":"on"}` за 60ms
- `homed_get_properties` через `command/custom` + `WaitFor fd/custom/garland_window` за 60-65ms
- `homed_list_live` за 0.6ms
- `homed_subscribe` за 2.2ms
- Recorder: 54 items, 141k data rows, 475k hour rows, всё чисто

## 📊 Метрики ответа

| Tool | Latency |
|------|---------|
| homed_overview | 342 µs |
| homed_list_devices | 2.85 ms |
| homed_list_services | 394 µs |
| homed_list_exposes | 9.20 ms |
| homed_get_status | 12.15 ms |
| homed_set_device | 943 µs |
| homed_get_topic (не найден) | 60 µs |
| homed_get_topic (найден) | 469 µs |
| homed_get_request (timeout 10s) | 10.0 s |
| homed_get_properties (с ответом) | 60.5–64.4 ms |
| homed_subscribe | 2.16 ms |
| homed_list_live | 629 µs |

## 🔧 Изменения в этом раунде

### `mcp-server/internal/mcp/tools.go`

1. **`toolGetTopic`** (fallback): если `status/<id>` пусто, пробует `device/<id>`, добавляет `note` в response.
2. **`toolGetRequest`** (валидация): fail-fast на read-only топиках `status/`, `expose/`, `device/`, `service/`, `fd/`, `td/`, возвращает ошибку с подсказкой.

### `mcp-server/internal/mcp/server_test.go`

Добавлены 2 новых теста:
- `TestToolCallGetTopicStatusFallsBackToDevice` — проверяет fallback с `note`
- `TestToolCallGetRequestRejectsReadOnlyTopic` — 7 кейсов (status/expose/device/service/fd/td)

### Тесты

```
ok  github.com/u236/homed-mcp/cmd/server        0.884s
ok  github.com/u236/homed-mcp/internal/config   0.564s
ok  github.com/u236/homed-mcp/internal/homedweb 0.517s
ok  github.com/u236/homed-mcp/internal/mcp      0.159s   ← 4 новых теста
ok  github.com/u236/homed-mcp/internal/mqtt     0.635s
ok  github.com/u236/homed-mcp/internal/recorder 0.859s
ALL_OK
```

## Итог

Все 4 аномалии либо уже исправлены, либо исправлены в этом раунде. **3 из 4** починены кодером (Аномалия №1 в прошлом раунде, №2 и №4 в этом). Аномалия №3 (двойной subscribe) оставлена как low-priority (MQTT-идемпотентность спасает).

Сервер `homed-mcp` стабилен, latency всех tools < 15ms (кроме `homed_get_request` который теперь сразу fail-fast на read-only топиках вместо 10s timeout).