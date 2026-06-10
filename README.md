# HOMEd MCP Server

`homed-mcp` — это [Model Context Protocol](https://modelcontextprotocol.io/)
сервер, написанный на Go. Он предоставляет языковым моделям инструменты для
взаимодействия с экосистемой умного дома [HOMEd](https://github.com/u236)
через MQTT брокер, к которому подключены сервисы HOMEd (homed-web,
homed-zigbee, homed-modbus, ...).

> Никакие файлы родительского проекта `homed-service-web` не изменяются —
> всё новое приложение расположено в каталоге `mcp-server/`.

## Возможности

Сервер регистрирует следующие MCP-инструменты:

| Инструмент | Назначение |
| --- | --- |
| `homed_overview` | Краткая сводка: количество и список устройств, сервисов, экспозиций и закэшированных статусов. |
| `homed_list_devices` | Список устройств с их friendly-name и свойствами (ретэйн `device/*`). |
| `homed_list_services` | Список запущенных сервисов HOMEd (ретэйн `service/*`). |
| `homed_list_exposes` | Список expose-устройств мостов (ретэйн `expose/*`). |
| `homed_get_status` | Получить последний закэшированный статус конкретного устройства либо всех сразу. |
| `homed_get_topic` | Получить сырой JSON по произвольному подтопику. |
| `homed_get_request` | Отправить запрос и дождаться ответа (использует топик `response/<id>`). |
| `homed_publish` | Опубликовать JSON-сообщение в произвольный топик (например, команду устройству). |
| `homed_set_device` | **Высокоуровневый**: управление устройством. Публикует `{property:value}` на топик `td/<service>/<deviceId>[/endpointId]` (HOMEd convention) — эквивалент переключения тумблера в homed-web. **Используйте именно его для on/off/toggle.** |
| `homed_subscribe` | Подписаться на топик с поддержкой MQTT-маток (`#` и `+`). |
| `homed_unsubscribe` | Отписаться от ранее добавленного топика. |
| `homed_get_properties` | Отправить `getProperties` сервису (`command/<service>`) и дождаться первого ответа в `fd/<service>/<device>`. Полезно для устройств, которые не публикуют ретэйн-статус. |
| `homed_list_live` | Показать снапшот **неретэйн-кеша** сообщений, пришедших по активным подпискам, с опциональным фильтром. |
| `homed_query_recorder` | **Исторические данные** из БД `homed-recorder`. Используйте для любых вопросов «как часто / когда / в среднем / самый холодный день» — `kind=stats` для одного агрегата за период, `kind=daily` для посуточных бакетов (например, «самый холодный день в марте»), `kind=transitions` для подсчёта вкл/выкл, `kind=items` для каталога записываемых параметров. |

## Управление устройствами (homed_set_device / homed_publish)

HOMEd-сервисы слушают управляющие команды на топиках вида
`td/<service>/<deviceId>[/endpointId]` (топик `to device`). Именно
их использует сам интерфейс `homed-web`, когда пользователь нажимает
тумблер или регулятор. Чтобы модели было удобно управлять
устройствами, `homed-mcp` предоставляет два инструмента:

* **`homed_set_device`** — высокоуровневый. Параметры
  `endpoint` + `property` + `value` (например, `endpoint=custom/alarm`,
  `property=status`, `value=on`) превращаются в публикацию
  `{"status":"on"}` на топике `td/custom/alarm` с флагом
  `retain=false`. Это **ровно** тот же формат, что использует
  `homed-web` для switch-expose, и `homed-zigbee` / `homed-modbus` /
  другие сервисы его принимают без модификаций. Для нестандартных
  команд предусмотрен параметр `message` — он перекрывает
  собранный `{property:value}`.

* **`homed_publish`** — низкоуровневый. Публикует произвольный
  JSON в произвольный топик. Для совместимости понимает несколько
  форм записи:
  - `device/<service>/<id>` — автоматически переписывается в
    `td/<service>/<id>` (старая форма из ранних версий MCP-сервера);
  - `<service>/<id>` — короткая форма, тоже переписывается в
    `td/<service>/<id>`;
  - `td/<service>/<id>` — публикуется как есть;
  - `command/...`, `status/...`, `expose/...`, `service/...`,
    `fd/...`, `response/...` — публикуются как есть, без редиректа.

  Для топиков `td/...` (то есть всегда, когда речь идёт об
  управлении устройством) флаг `retained` **принудительно
  сбрасывается в `false`**: ретэйн на `td/...`-топиках ломает
  нормальную работу устройств.

Для запроса текущего состояния используйте `homed_get_status`
(если устройство публикует ретэйн `status/<device>`) либо
`homed_get_properties` (если сервис отдаёт состояние только в
ответ на `command/<service>`).

## Пользовательские имена из homed-web

Если в `config.json` указать путь к `database.json` (homed-web), то
инструменты, возвращающие сведения об устройствах и expose-топиках,
дополняются **пользовательскими именами**, которые пользователь задал
в интерфейсе homed-web (имя помещения, имя элемента, имя статуса).
Это особенно удобно для моделей — модель видит не сырое
`custom/61226326-10251872/expose/OTget25`, а понятное
«Офис / котел / Подача».

Конфигурация:

```json
"paths": {
  "homed-web": "/opt/homed-web/database.json"
}
```

Когда файл загружен, в ответах следующих инструментов появляется
поле `usage` или `meta`:

| Инструмент | Поле | Содержимое |
| --- | --- | --- |
| `homed_list_devices`    | `usage` | список дашбордов/блоков, в которых встречается endpoint |
| `homed_list_exposes`    | `usage` | то же для expose-устройств |
| `homed_get_status`      | `meta[endpoint]` | `usage` + сопоставление `status_<N>` → пользов. имя |
| `homed_get_topic`       | `meta` | `usage` + (для `status/*`) словарь имён ключей |
| `homed_get_request`     | `meta.usage` | дашборды/блоки для запрошенного endpoint'а |
| `homed_get_properties`  | `usage` | дашборды/блоки для пары `service/device` |

Формат записи `usage`:

```json
{
  "dashboard": "Офис",
  "block": "котел",
  "item": {
    "endpoint": "custom/61226326-10251872",
    "expose":   "OTget25",
    "name":     "Подача"
  }
}
```

- `dashboard` — логическая группа из `dashboards[].name` («Свет», «Котел»).
- `block` — помещение/подсистема из `dashboards[].blocks[].name` («котел», «Климат»).
- `item` — пользовательское имя конкретного `expose`/`property` из `blocks[].items[].name`.

Имена для отдельных ключей статуса (`status_2`, `status_15` и т.п.)
считываются из верхнеуровневого словаря `database.json.names`
(например, `"custom/14705744-45074752/status_2": "🚰ГВС"`) и
отображаются в виде объекта `"names"` в `meta`.

## Флаг `names` службы (zigbee/matter/ble/custom)

При старте каждая служба HOMEd публикует в ретэйне
`{prefix}/status/<service>` JSON-объект, в котором среди прочего
присутствует ключ `names`:

| `names` | Что лежит в пути MQTT-топика после `{prefix}/<kind>/<service>/` |
| --- | --- |
| `false` (по умолчанию) | **id** устройства (`nodeId`, `id`, `MAC-адрес` и т.п.) |
| `true` | **имя** устройства (поле `name` из JSON-описания) |

Например, для одной и той же службы `custom`:

```text
names:false -> {prefix}/device/custom/61226326-10251872
              {prefix}/expose/custom/61226326-10251872
              {prefix}/status/custom/61226326-10251872
              {prefix}/td/custom/61226326-10251872
              {prefix}/fd/custom/61226326-10251872

names:true  -> {prefix}/device/custom/OpenTherm
              {prefix}/expose/custom/OpenTherm
              {prefix}/status/custom/OpenTherm
              {prefix}/td/custom/OpenTherm
              {prefix}/fd/custom/OpenTherm
```

`homed-mcp` автоматически отслеживает флаг `names` для каждой
службы и использует его при работе с устройствами:

* В ответах `homed_list_devices`, `homed_list_exposes` и
  `homed_get_status.meta[<endpoint>]` появляются поля
  `id`, `name`, `service`, `usesNames` — модель сразу видит,
  каким идентификатором оперирует служба.
* В `homed_overview` добавляется сводная таблица вида
  `custom -> uses name in topic paths` /
  `zigbee -> uses id in topic paths`.
* При вызове `homed_set_device` (и `homed_publish` с топиком
  `td/...`) указанный `endpoint` вида `custom/61226326-10251872`
  автоматически переводится в актуальный для брокера путь
  (`custom/OpenTherm`), если служба работает с `names:true`.
  При невозможности сопоставить id и name выводится
  предупреждение, а топик остаётся в исходном виде — брокер
  молча отбросит команду, но пользователь увидит, почему.

Если у службы ещё не было ретэйна `status/<service>`,
`homed-mcp` по умолчанию считает `names:false` (это
обратно-совместимое историческое поведение HOMEd).

## Статистика из homed-recorder (homed_query_recorder)

Когда в `config.json` указан путь к файлу БД `homed-recorder.db`,
регистрируется дополнительный инструмент `homed_query_recorder`,
который отвечает на вопросы вида

* «какая температура была сегодня ночью на улице»,
* «какой был самый холодный день в апреле»,
* «сколько раз включался насос за последние сутки»,
* «сколько сегодня работал котёл»,

то есть на любые запросы, требующие агрегации по архивным данным.
База открывается **только на чтение** — `homed-mcp` не может
испортить живые данные.

```json
"paths": {
  "homed-web":      "/opt/homed-web/database.json",
  "homed-recorder": "/opt/homed-recorder/homed-recorder.db"
}
```

### Параметры

| Поле | Тип | Назначение |
| --- | --- | --- |
| `kind` | `stats` (по умолчанию) \| `daily` \| `transitions` \| `items` | Что именно вычислить. |
| `endpoint` | string | Шаблон endpoint'а устройства. Поддерживает префиксное совпадение (`zigbee` найдёт все устройства zigbee), точное совпадение (`zigbee/0x...`), пусто = все устройства. |
| `property` | string | Имя свойства (`temperature`, `status`, `FlameDuration`, ...). Пусто = все свойства. |
| `metric` | `avg` (по умолчанию) \| `min` \| `max` \| `sum` \| `count` \| `first` \| `last` \| `extrema` | Агрегатная функция (для `kind=stats`). |
| `series` | `hour` (по умолчанию) \| `data` | Источник: пред-агрегированные часовые бакеты или сырые отсчёты. |
| `from` | string | Начало диапазона. Ключевые слова: `today`, `yesterday`, `this-week`, `this-month`, `last-24h`, `last-7d`, `last-30d`, `now`. Также RFC3339 и `YYYY-MM-DD`. |
| `to` | string | Конец диапазона (exclusive). Те же форматы, что и `from`. Пусто = сейчас. |
| `limit` | int (default 50) | Макс. число событий перехода, возвращаемых в `kind=transitions`. |

### Примеры вызовов

```json
// Средняя температура за вчерашний день (по всем устройствам с property=temperature)
{"kind":"stats","endpoint":"zigbee","property":"temperature","metric":"avg","from":"yesterday","to":"today"}

// Самый холодный день в апреле
{"kind":"daily","endpoint":"zigbee","property":"temperature","metric":"min","from":"2026-04-01","to":"2026-05-01"}

// Сколько раз за сутки включался насос
{"kind":"transitions","endpoint":"custom/pump","property":"status","from":"last-24h"}

// Сколько секунд сегодня работал котёл
{"kind":"stats","endpoint":"custom/boiler","property":"FlameDuration","metric":"sum","series":"hour","from":"today"}

// Какие устройства вообще есть в базе
{"kind":"items"}
```

Ответы — JSON с обогащением `usage` (если загружен `homed-web`).
Поле `usage` содержит те же дашборды/блоки/имена, что и в живых
инструментах, так что модель видит не `custom/61226326/FlameDuration`,
а `Котел / котёл / Наработка горелки`.

Если путь `homed-recorder` не указан или файл недоступен, инструмент
регистрируется, но любой вызов возвращает
`error: homed-recorder database is not configured …`.

## Транспорты

`homed-mcp` умеет работать в двух режимах, выбираемых полем `transport`
(или флагом `-http-addr` для обратной совместимости):

| Режим | Когда используется | Как подключается |
| --- | --- | --- |
| **stdio** (`transport: "stdio"`, по умолчанию) | MCP-клиент запускает бинарь как дочерний процесс. | Newline-delimited JSON-RPC 2.0 через `stdin`/`stdout`. |
| **Streamable HTTP** (`transport: "streamableHttp"`) | Бинарь запущен на отдельной машине (например, на роутере), к которой обращаются по сети. | `POST` / `GET` / `DELETE` на эндпоинте `/mcp` по спецификации MCP `2025-03-26`. |

### Запуск в режиме stdio

MCP-клиент (Cline, Claude Desktop, Continue и т.п.) стартует процесс сам.
Пример конфигурации клиента:

```json
{
  "mcpServers": {
    "homed": {
      "command": "C:\\path\\to\\homed-mcp.exe",
      "args": [
        "-broker", "tcp://192.168.0.10:1883",
        "-prefix", "homed"
      ],
      "env": {
        "HOMED_MQTT_USERNAME": "homed",
        "HOMED_MQTT_PASSWORD": "secret"
      }
    }
  }
}
```

### Запуск в режиме Streamable HTTP

```bash
./homed-mcp -broker tcp://192.168.0.10:1883 -prefix homed -http-addr :8082
```

либо через файл конфигурации (см. ниже):

```bash
./homed-mcp -config /etc/homed-mcp/config.json
```

После старта бинарь слушает `http://<host>:<port>/mcp`. На эндпоинте
`GET /healthz` возвращается `200 OK`, на `GET /` — статическая HTML-страница
со списком инструментов.

Конфигурация Cline для удалённого режима (`type: streamableHttp`):

```json
{
  "mcpServers": {
    "homed": {
      "disabled": false,
      "timeout": 30,
      "type": "streamableHttp",
      "url": "http://192.168.0.15:8082/mcp"
    }
  }
}
```

Если конкретная версия Cline ещё не знает тип `streamableHttp`, используйте
`type: sse` с тем же URL — MCP-сервер отдаст ответы как `text/event-stream`
и legacy SSE-клиент будет работать.

#### Сводка HTTP-эндпоинтов

| Метод | Путь | Описание |
| --- | --- | --- |
| `POST` | `/mcp` | Принимает одно JSON-RPC сообщение или массив. Если в `Accept` есть `text/event-stream` — отвечает SSE, иначе — `application/json`. На запрос `initialize` создаёт сессию и возвращает её в заголовке `Mcp-Session-Id`. |
| `GET` | `/mcp` | Открывает SSE-стрим для серверных уведомлений. Требует заголовки `Accept: text/event-stream` и `Mcp-Session-Id`. Периодически шлёт `: ping` для keep-alive. |
| `DELETE` | `/mcp` | Закрывает сессию, указанную в `Mcp-Session-Id`. |
| `GET` | `/healthz` | Liveness-проверка. |
| `GET` | `/` | Краткая HTML-страница со статусом. |

Пример `curl`-вызова:

```bash
# 1. Инициализация (получим Mcp-Session-Id)
curl -sS -X POST http://192.168.0.15:8082/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"curl","version":"1"}}}'

# 2. Дальнейшие вызовы с указанием сессии
curl -sS -X POST http://192.168.0.15:8082/mcp \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json' \
  -H 'Mcp-Session-Id: <id-из-шага-1>' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'
```

## Конфигурация

Все настройки `homed-mcp` задаются единым деревом параметров, которое
может прийти из **трёх источников одновременно**:

1. JSON-файл конфигурации (путь задаётся флагом `-config` или переменной
   `HOMED_MCP_CONFIG`; по умолчанию используется `./config.json` рядом с
   бинарём, если файл существует).
2. Переменные окружения.
3. Аргументы командной строки (флаги).

При старте сервер объединяет значения в порядке приоритета
(от высшего к низшему):

1. Флаг командной строки
2. Переменная окружения
3. Поле в JSON-файле конфигурации
4. Встроенное значение по умолчанию

### Файл конфигурации

Пример `config.json` (полный список полей) лежит в репозитории —
`mcp-server/config.example.json`:

```json
{
  "transport": "streamableHttp",
  "http":    { "addr": ":8082" },
  "mqtt":    { "broker": "tcp://localhost:1883", "prefix": "homed" },
  "paths": {
    "homed-web":      "/opt/homed-web/database.json",
    "homed-recorder": "/opt/homed-recorder/homed-recorder.db"
  }
}
```

| Поле JSON | Флаг | Переменная | Назначение |
| --- | --- | --- | --- |
| `transport` | `-transport` | `HOMED_MCP_TRANSPORT` | Транспорт MCP: `stdio` (по умолчанию) или `streamableHttp`. |
| `http.addr` | `-http-addr` | `HOMED_MCP_HTTP_ADDR` | Адрес HTTP-сервера, например `:8082`. Обязателен при `transport=streamableHttp`; установка `-http-addr` автоматически переключает транспорт в `streamableHttp`. |
| `mqtt.broker` | `-broker` | `HOMED_MQTT_BROKER` | URL MQTT-брокера (по умолчанию `tcp://localhost:1883`). |
| `mqtt.username` | `-username` | `HOMED_MQTT_USERNAME` | Имя пользователя брокера. |
| `mqtt.password` | `-password` | `HOMED_MQTT_PASSWORD` | Пароль брокера. |
| `mqtt.prefix` | `-prefix` | `HOMED_MQTT_PREFIX` | Префикс топиков HOMEd (по умолчанию `homed`). |
| `mqtt.clientId` | `-client-id` | `HOMED_MQTT_CLIENT_ID` | Идентификатор MQTT-клиента (случайный, если не задан). |
| `paths.<name>` | `-path-<name> <value>` | `HOMED_PATH_<KEY>` | Произвольная плоская карта «имя → путь к локальному файлу». Задаётся в `paths.<name>` в JSON, через `-path-<name>` на CLI, либо через переменную окружения `HOMED_PATH_<KEY>`. Ключ чувствителен к регистру по суффиксу, но не к разделителю (`HOMED_PATH_HOMED_WEB` → `paths.homed_web`, не `paths.homed-web`). См. раздел ниже. |
| — | `-config` | `HOMED_MCP_CONFIG` | Путь к JSON-файлу конфигурации. |
| — | `-version` | — | Вывести версию и выйти. |
| — | `-h`, `-help` | — | Показать встроенную справку и выйти. |

### Пример запуска через файл конфигурации

```bash
# 1. Положить config.json рядом с бинарём (или указать -config /path/to/config.json)
cp mcp-server/config.example.json /etc/homed-mcp/config.json
$EDITOR /etc/homed-mcp/config.json

# 2. Запустить
./homed-mcp -config /etc/homed-mcp/config.json
```

В лог при старте выводится итоговая конфигурация:

```
[homed-mcp] config: loaded /etc/homed-mcp/config.json
[homed-mcp] config: transport=streamableHttp, mqtt.broker=tcp://192.168.0.10:1883, mqtt.prefix=homed
```

### Приоритеты — пример

```bash
# В config.json: mqtt.broker = tcp://broker.local:1883
# В окружении:   HOMED_MQTT_BROKER=tcp://10.0.0.1:1883
# Флаг:          -broker tcp://10.0.0.99:1883

./homed-mcp
# Итог: mqtt.broker = tcp://10.0.0.99:1883   (флаг выиграл)
```

### Сценарии запуска

- **stdio (по умолчанию)** — `transport=stdio`, `http.addr` пуст. Запускается
  MCP-клиентом как подпроцесс. Все настройки брокера берутся из
  конфигурации/переменных окружения; флаги и файл конфигурации не
  обязательны.
- **Streamable HTTP** — установить `transport=streamableHttp` и
  `http.addr=":8082"` (например, в `config.json`). Бинарь слушает
  `http://<host>:<port>/mcp`.

### Обратная совместимость

Старые флаги и переменные окружения (`-broker`, `-username`, `-http-addr`,
`HOMED_MQTT_*`, `HOMED_MCP_HTTP_ADDR`) продолжают работать. Указание
`-http-addr` автоматически переключает транспорт в `streamableHttp`,
как и раньше. Поведение по умолчанию (stdio, `tcp://localhost:1883`,
префикс `homed`) полностью сохранено.

## Пути к локальным файлам

Секция `paths` файла конфигурации описывает расположение **локальных
JSON- и других файлов** на машине, на которой запущен `homed-mcp`.
Типичный пример — пути к базам данных и логам сервисов HOMEd, на
которые придётся ссылаться из MCP-инструментов.

`paths` — это **плоская карта** `имя → путь` с произвольным набором
ключей. Никакой вложенной иерархии, никаких дефолтов с магической
«подстановкой корня»: просто пары «короткое имя → абсолютный или
относительный путь». MCP-сервер не открывает эти файлы при старте —
он лишь регистрирует карту, валидирует формат и логирует её.

### Пример

```json
{
  "paths": {
    "homed-web":      "/opt/homed-web/database.json",
    "homed-recorder": "/opt/homed-recorder/homed-recorder.db"
  }
}
```

### Задание путей из разных источников

Один и тот же путь можно задать тремя способами, старшинство
стандартное (флаг > env > JSON > default):

1. **JSON-файл конфигурации** (рекомендуется):

   ```json
   "paths": {
     "homed-web":      "/opt/homed-web/database.json",
     "homed-recorder": "/opt/homed-recorder/homed-recorder.db"
   }
   ```

2. **Переменная окружения** `HOMED_PATH_<KEY>` — удобно для
   Docker/systemd, не требует правки `config.json`:

   ```bash
   export HOMED_PATH_HOMED_WEB=/opt/homed-web/database.json
   export HOMED_PATH_HOMED_RECORDER=/opt/homed-recorder/homed-recorder.db
   ./homed-mcp
   ```

   Имя в `paths` получается из суффикса переменной после
   `HOMED_PATH_`, в нижнем регистре. Регистр по суффиксу сохраняется,
   разделители (`-` vs `_`) — тоже: `HOMED_PATH_HOMED_WEB` →
   `paths.homed_web` (а не `paths.homed-web`). Если в JSON-файле уже
   задан `paths.homed-web`, env-переменная `HOMED_PATH_HOMED_WEB`
   создаст **отдельный** ключ `paths.homed_web`; они не
   пересекаются.

3. **Флаги командной строки** `-path-<name> <value>` — можно
   передавать сколько угодно, каждый создаёт или перезаписывает одну
   запись в `paths`:

   ```bash
   ./homed-mcp \
     -broker tcp://localhost:1883 \
     -path-homed-web      /opt/homed-web/database.json \
     -path-homed-recorder /opt/homed-recorder/homed-recorder.db
   ```

   Допускается и «=»-форма: `-path-homed-web=/opt/.../database.json`.

### Что попадает в лог при старте

Каждая запись `paths` печатается отдельной строкой, отсортированной по
имени для стабильного вывода:

```
[homed-mcp] config: loaded /etc/homed-mcp/config.json
[homed-mcp] config: transport=streamableHttp, mqtt.broker=tcp://192.168.0.10:1883, mqtt.prefix=homed
[homed-mcp] paths: homed-recorder=/opt/homed-recorder/homed-recorder.db
[homed-mcp] paths: homed-web=/opt/homed-web/database.json
```

## Сборка

Требуется Go 1.24+.

```bash
cd mcp-server
# linux/arm, GOARM=7 — для роутера
GOOS=linux GOARCH=arm GOARM=7 go build -o homed-mcp ./cmd/server

# windows/amd64
go build -o homed-mcp.exe ./cmd/server

# linux/amd64
GOOS=linux GOARCH=amd64 go build -o homed-mcp-amd64 ./cmd/server
```

Или через Docker:

```bash
docker build -t homed-mcp mcp-server
```

## Архитектура

```
mcp-server/
├── cmd/server/main.go          # точка входа: загрузка конфига + stdio либо HTTP
├── internal/
│   ├── config/                 # загрузка JSON-файла, env, флагов
│   │   ├── config.go           # Config, Transport, MQTT, HTTP
│   │   └── config_test.go      # юнит-тесты загрузчика
│   ├── mcp/                    # реализация MCP
│   │   ├── server.go           # JSON-RPC 2.0 диспетчер (stdio)
│   │   ├── http.go             # Streamable HTTP транспорт
│   │   ├── types.go            # описание JSON-RPC и MCP типов
│   │   ├── tools.go            # регистрация HOMEd-инструментов
│   │   ├── recorder_tool.go    # homed_query_recorder
│   │   ├── match.go            # матчинг MQTT wildcards
│   │   ├── server_test.go      # юнит-тесты stdio-транспорта
│   │   ├── http_test.go        # юнит-тесты HTTP-транспорта
│   │   └── recorder_tool_test.go # юнит-тесты homed_query_recorder
│   └── mqtt/                   # Paho-обёртка с кэшем ретэйн-сообщений
│       ├── client.go
│       ├── client_test.go
│       └── id.go
├── config.example.json         # пример файла конфигурации
├── go.mod
├── go.sum
├── Dockerfile
└── README.md
```

Связь `internal/mcp` и `internal/mqtt` идёт через интерфейс `mcp.MQTTClient`,
поэтому слой инструментов легко тестировать с фейковым клиентом.
Диспетчер методов (`Server.handle`) переиспользуется обоими транспортами:
stdio читает фреймы из `bufio.Reader`, HTTP — из тела `POST`-запроса.

## Протокол

Реализовано подмножество MCP (`initialize`, `tools/list`, `tools/call`,
`ping`, `notifications/initialized`) совместимое со спецификацией
`2024-11-05`. HTTP-транспорт дополнительно поддерживает `2025-03-26`
(Streamable HTTP). Кадры JSON-RPC 2.0 в stdio разделяются символом новой
строки; в HTTP одиночные сообщения и батчи принимаются в теле `POST`.

## Тесты

```bash
cd mcp-server
go test ./...
```

Покрытие:

- `TestInitialize`, `TestToolsList`, `TestToolCallListDevices`,
  `TestToolCallPublish` — stdio-транспорт и регистрация инструментов.
- `TestPathMatch` — MQTT-матчинг `#` и `+` для подписок.
- `TestTopicMatchesMQTT` — MQTT-матчинг для кэша неретэйн-сообщений.
- `TestHTTPNonStreamable`, `TestHTTPSSE`, `TestHTTPToolsList`,
  `TestHTTPDeleteSession`, `TestHTTPHealthz`, `TestHTTPPingBatch`,
  `TestRunHTTPShutdown` — Streamable HTTP транспорт.
- `TestDefault`, `TestTransportValid`, `TestLoadFlagsOnly`, `TestLoadFile`,
  `TestFlagOverridesFile`, `TestEnvOverridesFileDefault`,
  `TestHTTPMissingAddr`, `TestUnknownTransport`,
  `TestHTTPAddrImpliesStreamableHTTP` — загрузчик конфигурации
  (`internal/config`).
- `TestPathsDefaultsAreEmpty`, `TestPathsFileLoadsMap`,
  `TestPathsEnvVarFillsEmpty`, `TestPathsEnvGeneratesMap`,
  `TestPathsFlagOverride`, `TestPathsFlagEqualsSyntax` — секция
  `paths` загрузчика (плоская карта «имя → путь» из JSON,
  переменных окружения `HOMED_PATH_<KEY>` и флагов
  `-path-<name>`).
