package mcp

import (
"sort"
"strings"
"unicode"
"unicode/utf8"
)

var roomAliases = []struct {
needles []string
room    string
}{
{[]string{"кухн", "kitchen", "kuxnya", "кухня"}, "kitchen"},
{[]string{"зал", "гостин", "гост", "zal", "hall"}, "hall"},
{[]string{"спальн", "spalnya", "bedroom"}, "bedroom"},
{[]string{"детск", "detska", "detsk", "kids", "spalnym", "spalnyaM"}, "kids"},
{[]string{"ванн", "сануз", "душ", "dush", "bath", "shower"}, "bath"},
{[]string{"коридор", "тамбур", "прихож", "koridor", "tambur", "hallway", "corridor"}, "corridor"},
{[]string{"веранда", "крыльц", "террас", "veranda", "terrace", "крыльцо"}, "veranda"},
{[]string{"бань", "sauna"}, "sauna"},
{[]string{"улиц", "нар", "двор", "outside", "outdoor"}, "outdoor"},
{[]string{"котёл", "котел", "boiler", "отоплен"}, "boiler"},
{[]string{"вод", "water", "водопровод"}, "water"},
{[]string{"электр", "electric", "щит"}, "electrical"},
{[]string{"свет", "light", "lighting"}, "lighting"},
{[]string{"график", "chart", "graph"}, "charts"},
}

var kindAliases = []struct {
needles []string
kind    string
}{
{[]string{"спот", "spot", "люстр", "lustra", "chandelier", "свет", "light", "торшер", "torsher", "led", "лампа", "лампоч"}, "light"},
{[]string{"розетк", "rozetk", "outlet", "socket"}, "outlet"},
{[]string{"выключатель", "vykluchatel", "switch", "btn"}, "switch"},
{[]string{"датчик движен", "присутств", "occupancy", "motion"}, "motion"},
{[]string{"дверь", "contact", "магнит", "magnet"}, "contact"},
{[]string{"температур", "temperature", "temp", "ds18b20", "otget", "blebedroom"}, "temperature"},
{[]string{"влажн", "humidity"}, "humidity"},
{[]string{"давлен", "pressure", "press"}, "pressure"},
{[]string{"горелк", "flame", "isflame", "modul"}, "boiler-flame"},
{[]string{"насос", "pump", "nasos"}, "pump"},
{[]string{"клапан", "valve"}, "valve"},
{[]string{"замок", "lock"}, "lock"},
{[]string{"штор", "cover", "curtain"}, "cover"},
{[]string{"батаре", "battery"}, "battery"},
{[]string{"напряж", "voltage"}, "voltage"},
{[]string{"ток", "current", "amperage"}, "current"},
{[]string{"мощност", "power"}, "power"},
{[]string{"энерг", "energy", "kwh"}, "energy"},
{[]string{"освещ", "illuminance", "lux"}, "illuminance"},
{[]string{"орощад", "signal", "rssi", "link"}, "signal"},
{[]string{"термостат", "thermostat"}, "thermostat"},
{[]string{"охрана", "alarm", "guard", "security"}, "alarm"},
{[]string{"гирлянд", "garland"}, "garland"},
{[]string{"уведомлен", "telegram", "notification"}, "notification"},
{[]string{"тариф", "tariff", "счёт", "counter", "счетч"}, "meter"},
{[]string{"счёт", "счет", "count"}, "counter"},
}

var emojiTable = []rune{
0x1F526, 0x1F4A1, 0x1F4A7, 0x1F321, 0x2668, 0x267B,
0x1F6B0, 0x2696, 0x1F4E3, 0x2705, 0x274C, 0x1F198,
0x1F525, 0x2796, 0x2728, 0x2B50, 0x1F31F, 0x1F6CB,
0x1FA9F, 0x1F6CF, 0x2700, 0x27BF,
}

func stripEmoji(s string) string {
bad := make(map[rune]bool, len(emojiTable))
for _, r := range emojiTable {
bad[r] = true
}
out := make([]rune, 0, len(s))
for _, r := range s {
if bad[r] {
continue
}
if r >= 0x1F300 && r <= 0x1FAFF {
continue
}
out = append(out, r)
}
return string(out)
}

func normaliseText(s string) string {
s = stripEmoji(s)
var b strings.Builder
b.Grow(len(s))
for _, r := range s {
switch {
case unicode.IsUpper(r):
b.WriteRune(unicode.ToLower(r))
case unicode.IsPunct(r) || unicode.IsSymbol(r):
default:
b.WriteRune(r)
}
}
return strings.TrimSpace(b.String())
}

func roomFromText(s string) string {
low := strings.ToLower(s)
norm := normaliseText(s)
for _, a := range roomAliases {
for _, n := range a.needles {
needle := strings.ToLower(n)
if strings.Contains(low, needle) || strings.Contains(norm, needle) {
return a.room
}
}
}
return ""
}

func kindFromText(s string, class string) string {
low := strings.ToLower(s + " " + class)
norm := normaliseText(s + " " + class)
for _, a := range kindAliases {
for _, n := range a.needles {
needle := strings.ToLower(n)
if strings.Contains(low, needle) || strings.Contains(norm, needle) {
return a.kind
}
}
}
return ""
}

func keywordsFor(s string) []string {
const maxTokens = 16
stop := stopWords()
norm := normaliseText(s)
seen := make(map[string]struct{}, 8)
out := make([]string, 0, 8)
for _, f := range strings.Fields(norm) {
if utf8.RuneCountInString(f) < 2 {
continue
}
if stop[f] {
continue
}
if _, ok := seen[f]; ok {
continue
}
seen[f] = struct{}{}
out = append(out, f)
if len(out) >= maxTokens {
break
}
}
return out
}

func stopWords() map[string]bool {
return map[string]bool{
"и": true, "в": true, "на": true, "по": true, "с": true, "со": true,
"из": true, "за": true, "для": true, "от": true, "до": true, "к": true,
"а": true, "но": true, "или": true, "the": true, "a": true, "an": true,
"of": true, "to": true, "for": true, "on": true, "in": true,
"at": true, "by": true, "and": true, "or": true,
}
}

type exposeLabel struct {
Expose   string         `json:"expose"`
Title    string         `json:"title"`
Room     string         `json:"room,omitempty"`
Kind     string         `json:"kind,omitempty"`
Class    string         `json:"class,omitempty"`
Unit     string         `json:"unit,omitempty"`
Type     string         `json:"type,omitempty"`
Keywords []string       `json:"keywords,omitempty"`
Command  map[string]any `json:"command,omitempty"`
Channel  int            `json:"channel,omitempty"`
Settable bool           `json:"settable,omitempty"`
}

func buildLabels(common map[string]any) []exposeLabel {
opts, _ := common["options"].(map[string]any)
items, _ := common["items"].([]any)
settableSet := make(map[string]bool, len(items))
for _, raw := range items {
if s, ok := raw.(string); ok {
settableSet[s] = true
}
}
out := make([]exposeLabel, 0, len(opts))
for key, raw := range opts {
m, _ := raw.(map[string]any)
if m == nil {
continue
}
title, _ := m["title"].(string)
title = strings.TrimSpace(title)
if title == "" {
continue
}
class, _ := m["class"].(string)
unit, _ := m["unit"].(string)
typ, _ := m["type"].(string)
room := roomFromText(title)
kind := kindFromText(title, class)
lbl := exposeLabel{
Expose:   key,
Title:    title,
Room:     room,
Kind:     kind,
Class:    class,
Unit:     unit,
Type:     typ,
Keywords: keywordsFor(title),
Settable: settableSet[key] && strings.HasPrefix(key, "switch_"),
}
if ch := channelFromExpose(key); ch > 0 {
lbl.Channel = ch
}
if lbl.Settable {
prop := "status_" + strings.TrimPrefix(key, "switch_")
lbl.Command = map[string]any{
"property": prop,
"value":    "on",
}
}
out = append(out, lbl)
}
sort.Slice(out, func(i, j int) bool { return out[i].Expose < out[j].Expose })
return out
}

func channelFromExpose(key string) int {
switch {
case strings.HasPrefix(key, "switch_"),
strings.HasPrefix(key, "status_"):
default:
return 0
}
idx := strings.LastIndexByte(key, '_')
if idx < 0 || idx == len(key)-1 {
return 0
}
n := 0
for _, r := range key[idx+1:] {
if r < '0' || r > '9' {
return 0
}
n = n*10 + int(r-'0')
}
if n == 0 {
return 0
}
return n
}

func roomsFromUsage(usage []map[string]any) []string {
seen := make(map[string]struct{}, 4)
for _, m := range usage {
dash, _ := m["dashboard"].(string)
if dash == "" {
continue
}
if r := roomFromText(dash); r != "" {
seen[r] = struct{}{}
}
}
out := make([]string, 0, len(seen))
for r := range seen {
out = append(out, r)
}
sort.Strings(out)
return out
}