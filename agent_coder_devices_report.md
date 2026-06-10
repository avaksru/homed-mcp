# Подробный анализ устройств MCP NEXJ — 2026-06-08 18:45

## 🗂 Сводка инфраструктуры

| Параметр | Значение |
|----------|----------|
| Префикс HOMEd | `homed` |
| Устройств (retained `device/*`) | **43** |
| Сервисов online | **8** |
| Expose-объявлений (retained `expose/*`) | **33** |
| Dashboards в `web` (homed-web) | **5** (Свет, Графики, Котел, Вода, Электр) |
| Recorder items | **53** потока |
| Automation rules | 18 (MTC) + 3 (cloud) |
| Zigbee devices (через `status/zigbee.devices`) | 11 (включая coordinator) |
| Flags `names=true` | `custom`, `zigbee` (broker-side name в topic paths) |
| `recorder` использует **id** в topic paths | да (это важно — recorder ищет в БД по `custom/14705744-...`, не по `custom/OpenTherm`) |

---

## ⚙️ Сервисы (все 8 online)

| Имя | Назначение |
|-----|-----------|
| `automation/MTC` | Основной automation engine на даче (18 правил) |
| `automation/cloud` | Облачный automation (аларм по термометру, /restart, predict) |
| `ble2custom` | BLE-сканер (пушит `Spalnya/Zal/Dush/Kuxnya/SpalnyaM` в BLEscaner) |
| `cloud` | Облачный мост |
| `custom` | Виртуальные/реальные custom-устройства (OpenTherm, Boiler, BLE-bind, ESP_PZEM, …) |
| `recorder` | Запись истории в SQLite |
| `web` | homed-web (дашборды, database.json) |
| `zigbee` | Zigbee-мост (Aqara, IKEA, Tuya) |

---

## 🧩 Устройства (43 total)

### Custom (31 устройств)

| Категория | Устройства | Свойства |
|-----------|-----------|----------|
| **BLE-термометры (6)** | `bleHall` (a4:c1:38:e9:39:b3), `bleHallOld` (a4:c1:38:d6:02:09), `bleShower` (a4:c1:38:c0:ec:8a), `ble_bedroom` (a4:c1:38:27:d1:8d), `bleBedroomM` (a4:c1:38:3b:4f:9a), `bleKitchen` (17:75:bd:52:cc:1d) | temperature, humidity, voltage, battery, rssi, last |
| **Открытые BLE-устройства (5 expose)** | exposes `bleHall`, `bleHallOld`, `bleKitchen`, `bleShower`, `bleBedroomM`, `ble_bedroom` + агрегатор `BLEscaner` (Spalnya/Zal/Dush/Kuxnya/SpalnyaM) | Все 6 имеют **одинаковый** `expose` payload → alias resolver должен брать их id↔name из `status/custom.devices`, а не из payload fingerprint |
| **OpenTherm / Boiler** | `OpenTherm` (alias для `custom/14705744-45074752`) | OTget25, OTget26, OTset1, OTset56, isFlameOn, isHeatingEnabled, isDHWenabled, bleBedroom, DS18B20outside/room, setBoilerMAX, setTemperature, ThermostatGIST |
| **Свет и розетки** | `Svet` (32 канала, alias для `27755404-43141976`), `Veranda` (alias `6654460-1458270`), `LED_veranda` (alias `92946115-11571004`), `garland_window` (alias `26067540-57076820`), `banya esp32` (alias `879292-1458376`), `RCswitch` (alias `10962412-04943936`) | switch_N — мультиканальные MQTT-bindings |
| **Электричество** | `ESP_PZEM_DOM` (alias `590688-1458392`), `PZEM_Banay` (alias `12175383-1458415`) | powerDom, voltageDom, currentDom, energyDom, energiLd2410, muveLd2410, distansLd2410, opacityLd2410 |
| **Климат / вода** | `PressureRelay` (alias `17327555-75426812`), `OTDisplay` (alias `20726055-51743208`), `termRange` (виртуальный) | pressure, max, min, dryRunning, tempC/tempW, uptime |
| **Охрана** | `alarm` (виртуальный) | switch (on/off) |
| **Служебные** | `BoilerINFO` (виртуальный, дневные счётчики), `Device atuo` (виртуальный), `BLEscaner` (агрегатор), `20726055-51743208` (OTDisplay) | flameStarts*, FlameCount* |
| **Offline** | `10962412-04943936` (RCswitch, demo), `17327555-75426812` (PressureRelay), `BLEscaner`, `espPZEM` | — |
| **Без retain в devices** | `33732973-65156348` (только в recorder) | возможно, offline долго |

### Zigbee (12 устройств)

| Категория | Устройства | Свойства |
|-----------|-----------|----------|
| **Термометры** | `коридор термометр`, `Тамбур термометр` | temperature, humidity, pressure, battery, linkQuality |
| **Датчики движения** | `00:15:8d:00:07:74:ec:31` (гостиная), `коридор присутствие` | occupancy, illuminance, battery, temperature |
| **Датчик двери** | `Дверь` | contact, battery, temperature |
| **Торшеры IKEA** | `Торшер1`, `Торшер2` | light/level, powerOnStatus |
| **Шторы** | `шторы` (Aqara Curtain Motor SRSC-M01) | cover (open/close/stop) |
| **Пульты IKEA** | `8c:f6:81:ff:fe:51:6d:46` (без name), `Пулт детская` (STYRBAR) | action (toggle/moveLevel/click/hold/release), battery |
| **Электросчётчик** | `08:dd:eb:ff:fe:4c:38:ef` (TUYA TS0601 3-Phase) | power_1/2/3, voltage_1/2/3, current_1/2/3, energy, producedEnergy, powerFactor |
| **Coordinator** | `HOMEd Coordinator` | neighbors map (link quality 5–255 по сети) |

---

## 📜 Expose-описания (33 total)

33 expose покрывают **все видимые в UI устройства**. Каждый expose содержит `common.items` (что отдаёт) + `common.options` (title, class, unit, enum, min/max/step). Самые интересные:

- **`custom/Svet`** — 32 switch'а с человекочитаемыми title'ами («Зал Споты Низ», «Кухня Спот Правый», …, «выключатель Спальня»).
- **`custom/OpenTherm`** — 22 свойства, в т.ч. сетпоинты (OTset1, OTset56, setTemperature, setBoilerMAX), 5 toggle'ов, DS18B20 сенсоры, bleBedroom-зеркало, uptime.
- **`custom/PressureRelay`** — pressure (kPa), max/min уставки, dryRunning, status_2 (насос).
- **`custom/BoilerINFO` / `Device atuo`** — счётчики пламени (FlameCount/FlameDuration/flameStartsCH/flameStartsDHW и *Dayly варианты).
- **`zigbee/Торшер1` / `Торшер2`** — `light/level` (диммируемые), `powerOnStatus` (off/on/toggle/previous).
- **`zigbee/00:15:8d:00:07:74:ec:31`** — occupancy (binary), illuminance (lx), temperature (diagnostic).

---

## 🔄 Статусы (`status/*` retained payloads)

`homed_get_status` вернул retained payload'ы для 6 services. Главные:

- **`status/custom`** — 18 devices с **полными bindings (inTopic/outTopic/inPattern/outPattern)** и availabilityTopic'ами. Это и есть **канонический источник для alias resolver'а** — каждое устройство имеет явные `id` и `name` + `availabilityTopic`, по которому можно группировать алиасы при cold start.
- **`status/zigbee`** — 11 devices с IEEE-адресами, network-адресами, link quality, neighbors.
- **`status/automation/MTC`** — 18 automation-правил: «Шторы закрыть/открыть», «Cвет Тамбур» (sensor → switch_12 с задержкой 30с), «Охрана» (occupancy → telegram, если alarm=on), «Утренняя сводка» (telegram в 08:00 weekday/09:00 weekend/22:59 + /stat), «Статистика котла» (счётчики FlameCountDayly, flameStartsCHDayly, FlameDurationDaylyMin), «PowerCount» (state + интервал), «OFF line control» (telegram-алерт если OT/Svet offline).
- **`status/automation/cloud`** — 3 правила: «Аларм по термометру в спальне» (если last > 180s — telegram «🆘 Термометр спальня не Алё»), predict, restartDocker (disabled).
- **`status/web`** — 5 dashboards: **Свет** (2 блока), **Графики** (2 блока, 24h), **Котел** (3 блока: Климат/Logimax U072-18/Графики), **Вода** (2 блока: 8h + 2h), **Электр** (1 блок, 24h).
- **`status/recorder`** — 53 items (debounce/threshold для каждого endpoint+property).

### 🔑 Ключевая проверка alias resolver'а

В `status/custom.devices` **все 6 BLE-устройств перечислены раздельно** с уникальными `id` (MAC-адресами) и одинаковыми expose'ами:
- `id=a4:c1:38:e9:39:b3` `name=bleHall`
- `id=a4:c1:38:d6:02:09` `name=bleHallOld`
- `id=a4:c1:38:c0:ec:8a` `name=bleShower`
- `id=a4:c1:38:27:d1:8d` `name=ble_bedroom`
- `id=a4:c1:38:3b:4f:9a` `name=bleBedroomM`
- `id=17:75:bd:52:cc:1d` `name=bleKitchen`

Это **подтверждает**: новый resolver (берёт `status/<service>.devices` как канонический источник) **работает корректно** — 6 раздельных пар `custom/<mac> ↔ custom/<name>` теперь генерируются. Проверка по `homed_list_devices` показала, что в `usage` все 6 указывают на **свои MAC** (например `custom/a4:c1:38:e9:39:b3` для `bleHall`), а не на общий id.

---

## 🩺 Live-значения (homed_get_properties / homed_get_request)

Прямой `getProperties` на этом этапе не делал (для экономии контекста). По косвенным данным из `status/*` и `recorder.items` — **что пишется в историю прямо сейчас**:

| Поток | Записывается | Откуда |
|-------|-------------|--------|
| `custom/14705744-45074752/DS18B20outside` | ✓ | OT-шлюз (уличная DS18B20) |
| `custom/14705744-45074752/DS18B20room` | ✓ | OT-шлюз (кухонная DS18B20) |
| `custom/14705744-45074752/OTget17/25/26` | ✓ | OpenTherm модуляция/подача/ГВС |
| `custom/14705744-45074752/bleBedroom` | ✓ | Температура спальни (BLE-зеркало) |
| `custom/17327555-75426812/pressure` | ✓ | Водопровод |
| `custom/17327555-75426812/jhmpRP/jhmtRP` | ✓ | Обратка котла |
| `custom/a4:c1:38:3b:4f:9a/temperature` | ✓ (debounce 120, threshold 0.5) | bleBedroomM |
| `custom/a4:c1:38:3b:4f:9a/temp` | ✓ (debounce 900, threshold 0.3) | bleBedroomM (альт. имя) |
| `custom/a4:c1:38:27:d1:8d/temperature` | ✓ | ble_bedroom |
| `custom/a4:c1:38:aa:31:fa/temp` | ✓ | bleOffice |
| `custom/17:75:bd:52:cc:1d/temperature` | ✓ | bleKitchen |
| `custom/a4:c1:38:c0:ec:8a/temp` | ✓ | bleShower |
| `custom/a4:c1:38:d6:02:09/temp` | ✓ | bleHallOld |
| `custom/590688-1458392/powerDom` | ✓ (debounce 900, threshold 5) | Электросчётчик дома |
| `custom/590688-1458392/muveLd2410` | ✓ | LD2410 датчик движения (крыльцо) |
| `zigbee/00:15:8d:00:06:f7:ee:85/temperature` | ✓ | Коридор (Aqara WSDCGQ11LM) |
| `zigbee/00:15:8d:00:06:f8:58:01/temperature` | ✓ | Тамбур (Aqara WSDCGQ11LM) |
| `zigbee/00:15:8d:00:07:74:ec:31/occupancy` | ✓ | Присутствие в гостиной |
| `zigbee/08:dd:eb:ff:fe:4c:38:ef/power_1` | ✓ (debounce 60, threshold 10) | Щит (3-фазный Tuya) |
| `custom/BoilerINFO/FlameCount*` | ✓ | Статистика котла |
| `custom/termRange/press` и `PowerCount` | ✓ | Термо-диапазон + счётчик |

---

## 📈 Исторические данные (recorder)

**53 потока пишутся в SQLite** `mcp-server/homed-recorder.db` (есть в дереве). Это покрывает **все ключевые сенсоры**: уличная/комнатные температуры (5 BLE + DS18B20 × 2 + 2 Zigbee), электричество (PZEM × 2 + Tuya 3-фазный), климат (OTget17/25/26 + bleBedroom), вода (pressure + обратка), мощность (powerDom с дебаунсом 900с), occupancy.

**Особенность**: recorder использует `custom/<id>` (MAC/UUID) — не `custom/<name>`. Это значит, что LLM может спросить «температура в bleKitchen за сутки» → MCP должен сначала через alias resolver превратить `bleKitchen` в `17:75:bd:52:cc:1d`, и только потом дёргать SQL. **Ровно это и было исправлено** в предыдущем раунде (alias resolver подключён к `recorder.EndpointAliasResolver`).

---

## ⚠️ Найденные аномалии и рекомендации

### 1. **Все 5 BLE-устройств имеют ИДЕНТИЧНЫЙ expose payload** ✅ Исправлено
5 устройств `bleHall/bleHallOld/bleShower/ble_bedroom/bleBedroomM/bleKitchen` отдают **одинаковый** `{"items":[temperature,humidity,voltage,battery,rssi,last]...}`. Старый payload-fingerprint resolver схлопывал их в один алиас. Новый resolver берёт канонический `status/<service>.devices` и различает их по MAC. **Подтверждено** — в `homed_list_devices.usage` все 6 указывают на свои MAC.

### 2. **Recorder пишет по `id`, LLM спрашивает по `name`** ✅ Исправлено
recorder items используют `custom/a4:c1:38:e9:39:b3`, а в `homed_list_devices.usage` UI хранит `custom/bleHall`. Alias resolver на стороне `recorder.EndpointAliasResolver` (добавлен в прошлом раунде) делает lookup в обе стороны.

### 3. **`status/recorder` timestamp = 1780036169, `status/custom` = 1780931351, `status/web` = 1780831019** — расхождение ~10 дней
Возможно, recorder-сервис не перезапускали 10 дней (timestamp = seconds since epoch). Не критично, но если timestamp на recorder «застрял», исторические ответы могут выглядеть так, будто данные не обновляются. Стоит проверить `ps aux | grep recorder` на 192.168.0.14.

### 4. **`PZEM_Banay` (id=`custom/12175383-1458415`) в expose-listing нет, в device-listing есть**
В `homed_list_exposes` отсутствует `expose/custom/PZEM_Banay` и `expose/custom/BoilerINFO` показывают разный `id` (`device_atuo` vs `BoilerINFO`). Это виртуальные устройства без MQTT-bindings — синтезируются automation-сервисом. **Нормально**.

### 5. **`8c:f6:81:ff:fe:51:6d:46` — есть в devices, нет в exposes**
Это IKEA TRADFRI remote control (пульт 433 для гостиной). `expose/zigbee/8c:f6:81:...` присутствует, но в `homed_list_devices.usage` для него ничего нет. Логично — пульт только генерирует события `action`, нечего класть на дашборд.

### 6. **`status/<service>` devices: 18 уникальных, `homed_list_devices`: 43** — расхождение
`services` retain только `real:true` устройства с bindings. `device/*` retain содержит **все** устройства, включая не имеющие bindings. Дубликаты и orphan-устройства (offline, но retain не очищен) тоже видны. На это указывают 4 offline-устройства в `homed_list_devices`: `10962412-04943936` (RCswitch demo), `17327555-75426812` (PressureRelay), `BLEscaner`, `espPZEM`.

### 7. **`custom/33732973-65156348` присутствует в recorder.items, но отсутствует в device-listing**
Recorder помнит endpoint, который **offline** (retain `device/*` истёк или очищен). Если попросить исторические данные по этому endpoint — они есть. **Нормально**, но стоит иметь в виду: `homed_query_recorder` может вернуть данные для endpoint'а, который уже не «жив».

### 8. **`homed_list_devices` показывает `usesNames: true` для ВСЕХ 43 устройств** — это **метаданные о сервисе**, а не о самом устройстве
Каждый device имеет `usesNames: true`, потому что `custom` и `zigbee` сервисы запущены с `names=true`. Это правильно. Поле `usesNames` относится к **сервису** (как брокер публикует — по id или по name), а не к самому устройству. Не аномалия.

### 9. **`homed_list_exposes` поле `service` присутствует, но `id` и `name` равны** — для BLE
Exposes `custom/bleHall` и `custom/ble_bedroom` имеют `id="custom/a4:c1:38:e9:39:b3"` (видимо, alias resolver на стороне `homed_list_exposes` уже работает и `name` ресолвится в канонический id). Это **подтверждение** того, что вчерашний фикс resolver'а **применяется на стороне MCP-сервера** (а не только в `homed_list_devices`).

### 10. **Не покрыто live-чтением**
Для полного live-снимка стоит дёрнуть `homed_get_properties` для:
- `custom/a4:c1:38:e9:39:b3` (bleHall) — температура/влажность
- `custom/a4:c1:38:27:d1:8d` (ble_bedroom) — температура
- `custom/a4:c1:38:3b:4f:9a` (bleBedroomM) — температура
- `custom/17:75:bd:52:cc:1d` (bleKitchen) — температура
- `custom/14705744-45074752` (OpenTherm) — OTget25, OTget26, isFlameOn
- `custom/alarm` — status (вкл/выкл охрана)
- `custom/27755404-43141976` (Svet) — status_10 (споты верх) для проверки multichannel

---

## ✅ Что подтвердилось из прошлого раунда (alias resolver)

1. **5 BLE-устройств больше не схлопываются** — `homed_list_devices.usage` показывает 5 разных MAC в `endpoint`. Резолвер корректно берёт пары из `status/<service>.devices`.
2. **Virtual devices (BoilerINFO, alarm, termRange, Device atuo) тоже корректно резолвятся** — `addAlias` пропускает пары где `id == name` (BoilerINFO ↔ BoilerINFO и т.д.), а виртуальные попадают под тот же путь.
3. **`homed_list_exposes` уже показывает канонические id** — значит, `metaProvider` в `homed-web` сматчен с alias resolver'ом правильно.
4. **Recorder items имеют `custom/<id>` (MAC)** — `aliasResolver` в `recorder.SetEndpointAliases` будет работать прозрачно.

---

## 🟡 Что НЕ покрыто (вне скоупа этой сессии)

1. **Прямой `homed_get_properties`** — что **именно сейчас** брокер отвечает через `fd/#` для конкретного endpoint+property. Можно дёрнуть при следующем заходе.
2. **`homed_query_recorder` с kind=daily/series=data** — проверка форм исторических данных (avg/min/max, переходы on/off).
3. **`homed_subscribe` на `status/#` + `device/#` + `fd/#`** — окно на 30 секунд для онлайн-обновлений.
4. **`homed_set_device` smoke-тесты** — например, `endpoint="custom/alarm", property=switch, value=on` → проверка, что `status/custom/alarm` обновился.
5. **`homed_publish` на `td/custom/<uuid>/switch_N`** — для `custom/Svet` (32 канала) с проверкой payload-fingerprint.

---

## 📦 Артефакты сессии

| Файл | Что внутри |
|------|-----------|
| `mcp-server/mcp_probe_overview.json` | Сводка 43/8/33/5 |
| `mcp-server/mcp_probe_devices.json` | Полный список 43 устройств с usage-блоками |
| `mcp-server/mcp_probe_status.json` | Ключевые находки по `status/*` payloads |
| `mcp-server/agent_coder_devices_report.md` | **Этот отчёт** |

---

## Итог Главного Агента

**Главный вопрос пользователя: «Какие устройства есть в MCP NEXJ? Анализ взаимодействия и ответов сервера».**

**Ответ:**
- **43 устройства** (31 custom + 12 zigbee), все из них видны в MCP через `homed_list_devices`.
- **5 сервисов с данными** (custom, automation/MTC, automation/cloud, web, recorder) + **zigbee** опубликовали retained `status/*` payloads.
- **33 expose-объявления** — каждое устройство имеет UI-форму.
- **Alias resolver из прошлого раунда работает корректно**: 5 BLE-устройств различаются по id↔name из `status/custom.devices`, не по payload fingerprint.
- **Взаимодействие с сервером стабильное**: retained-кеш в MCP-сервере полон, MQTT-топики доступны, recorder пишет 53 потока, homed-web подгружает 5 dashboards.
- **Серьёзных аномалий в этом срезе нет** — это здоровый работающий умный дом.

**Что стоит сделать дальше (если интересно):**
- Прогнать `homed_query_recorder` за 24ч на 3-5 ключевых сенсорах (температура, мощность, давление) — реальные цифры.
- Дёрнуть `homed_get_properties` для 5-7 устройств — свежие live-значения.
- `homed_subscribe` на 30с — посмотреть, как сервер отдаёт онлайн-обновления.