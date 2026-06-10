# HOMEd MCP Server

`homed-mcp` — сервер **Model Context Protocol (MCP)** для управления умным домом HOMEd через MQTT. Позволяет языковым моделям (Claude, Cline, Continue и др.) видеть устройства, читать состояние, управлять ими и запрашивать исторические данные.

---

## Основные возможности

| Инструмент | Назначение |
|------------|------------|
| `homed_overview` | Быстрая сводка: устройства, сервисы, статусы |
| `homed_list_devices` | Список всех устройств с понятными именами |
| `homed_list_exposes` | Устройства мостов (Zigbee, Matter и др.) |
| `homed_get_status` | Текущее состояние устройства |
| `homed_set_device` | **Управление** — вкл/выкл, регулировка (используйте этот!) |
| `homed_query_recorder` | **История**: средние, мин/макс, счётчики вкл/выкл, время работы |

> **Важно:** Для управления используйте `homed_set_device` — он публикует команды в правильном формате (`td/<service>/<id>`), как делает homed-web.

---

## Быстрый старт

### 1. Конфигурация (`config.json`)

```json
{
  "transport": "stdio",
  "mqtt": { "broker": "tcp://192.168.0.10:1883", "prefix": "homed" },
  "paths": {
    "homed-web":      "/opt/homed-web/database.json",
    "homed-recorder": "/opt/homed-recorder/homed-recorder.db"
  }
}
```

Поля:
- `mqtt.broker` — адрес MQTT-брокера
- `mqtt.prefix` — префикс топиков HOMEd (обычно `homed`)
- `paths.homed-web` — путь к `database.json` от homed-web (даёт понятные имена: «Офис / котел / Подача» вместо `custom/...`)
- `paths.homed-recorder` — путь к БД истории (для `homed_query_recorder`)

### 2. Запуск

**Локально (stdio) — для Cline / Claude Desktop:**
```json
{
  "mcpServers": {
    "homed": {
      "command": "C:\\path\\to\\homed-mcp.exe",
      "args": ["-config", "C:\\path\\to\\config.json"]
    }
  }
}
```

**На сервере/роутере (HTTP):**
```bash
# В config.json: transport: "streamableHttp", http: { "addr": ":8082" }
./homed-mcp -config /etc/homed-mcp/config.json
```
Потом в Cline:
```json
{ "mcpServers": { "homed": { "type": "streamableHttp", "url": "http://192.168.0.15:8082/mcp" } } }
```

---

## Примеры использования

### Включить/выключить устройство
```json
{
  "endpoint": "custom/boiler",
  "property": "status",
  "value": "on"
}
```
`homed_set_device` сам отправит `{"status":"on"}` в топик `td/custom/boiler`.

### Прочитать температуру
```json
{ "endpoint": "zigbee/0x1234", "property": "temperature" }
```
Используйте `homed_get_status` (если есть retained-статус) или `homed_get_properties`.

### История (homed_query_recorder)

| Вопрос | Параметры |
|--------|-----------|
| Средняя температура вчера | `{"kind":"stats","endpoint":"zigbee","property":"temperature","metric":"avg","from":"yesterday","to":"today"}` |
| Самый холодный день в апреле | `{"kind":"daily","endpoint":"zigbee","property":"temperature","metric":"min","from":"2026-04-01","to":"2026-05-01"}` |
| Сколько раз включался насос за сутки | `{"kind":"transitions","endpoint":"custom/pump","property":"status","from":"last-24h"}` |
| Секунд работы котла сегодня | `{"kind":"stats","endpoint":"custom/boiler","property":"FlameDuration","metric":"sum","series":"hour","from":"today"}` |

Поддерживаемые периоды: `today`, `yesterday`, `this-week`, `this-month`, `last-24h`, `last-7d`, `last-30d`, `now` или даты `YYYY-MM-DD`.

---

## Переменные окружения (альтернатива config.json)

```bash
HOMED_MQTT_BROKER=tcp://192.168.0.10:1883
HOMED_MQTT_PREFIX=homed
HOMED_MQTT_USERNAME=homed
HOMED_MQTT_PASSWORD=secret
HOMED_PATH_HOMED_WEB=/opt/homed-web/database.json
HOMED_PATH_HOMED_RECORDER=/opt/homed-recorder/homed-recorder.db
```

Приоритет: флаги CLI > env > config.json > дефолты.

---

## Сборка

```bash
# Windows
go build -o homed-mcp.exe ./cmd/server

# Linux ARM (роутер)
GOOS=linux GOARCH=arm GOARM=7 go build -o homed-mcp ./cmd/server

# Docker
docker build -t homed-mcp .
```

---

## Полезное

- Без `homed-web` устройства будут с техническими ID (`custom/61226326-...`). Добавьте `paths.homed-web` — модель будет видеть понятные имена.
- База `homed-recorder` открывается **только на чтение** — данные не испортятся.
- Для отладки: `homed_list_live` показывает live-сообщения, `homed_get_topic` — сырой JSON любого топика.

---

## Лицензия

MIT