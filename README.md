# HOMEd MCP Server

`homed-mcp` — сервер **Model Context Protocol (MCP)** для управления умным домом HOMEd через AI. Позволяет языковым моделям (Claude, DeepSeek, Copilot, GPTchat, Grok и др.) видеть устройства, читать состояние, управлять ими и запрашивать исторические данные.

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


---

## Быстрый старт

### Установка

✅ **Автоматическая установка одной командой:**

Для обычного Linux (Debian, Ubuntu и т.д.):
```bash
curl -s https://raw.githubusercontent.com/avaksru/homed-mcp/master/install.sh | sudo sh
```

Для OpenWrt:
```bash
curl -s https://raw.githubusercontent.com/avaksru/homed-mcp/master/install.sh | sh
```

**Скомпилировать из исходников:**
```bash
git clone https://github.com/avaksru/homed-mcp.git
cd homed-mcp
go build -o homed-mcp ./cmd/server
```

Или скачать готовый бинарник со [страницы релизов](https://github.com/avaksru/homed-mcp/releases).

---

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
Потом в Агенте LLM:
```json
{ "mcpServers": { "homed": { "type": "streamableHttp", "url": "http://192.168.0.15:8082/mcp" } } }
```

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