# Contratos de `wapp-edge-intent`

Todo lo que otros consumen de esta pieza, y todo lo que ella consume de fuera.

> **De dónde sale cada lista.** Este repo **no es un servicio**: no hay fichero de registro de
> rutas que grepear. Las listas se construyeron así:
> - **Rutas HTTP consumidas**: leyendo las URL literales compuestas en `ollama/client.go`
>   (`c.baseURL + "/api/..."`). Regla de conteo: **una URL literal compuesta = una ruta**.
> - **API pública**: `go doc -all ./ollama` y `go doc -all ./classifier`. Regla: **símbolo
>   exportado = entrada de la tabla**.
> - **Quién la usa de verdad**: `rg -n 'ollama\.[A-Z]\w*'` y `rg -n 'classifier\.[A-Z]\w*'` sobre
>   `wapp-edge-agent`, **excluyendo `_test.go` y comentarios**. Regla: **se cuenta por llamante
>   ejecutado, no por fichero que importa** — que es justo la distinción que hizo caducar al
>   ADR-0020.
> - **Variables de entorno**: `rg -n 'os\.Getenv|LookupEnv'` sobre todo el repo.

---

## 1. Rutas HTTP que SIRVE: **cero**

Este repo **no expone ni una sola ruta**. No arranca servidor, no abre puerto, no registra
handlers. `rg -n 'http.ListenAndServe|http.NewServeMux|gin\.|chi\.' .` → vacío.

## 2. Rutas HTTP que CONSUME: **cuatro**, todas contra el Ollama local

El host lo fija el llamante; el default del ecosistema es `http://127.0.0.1:11434` (lo pone
`wapp-edge-agent/internal/infra/config/config.go:563`). Siempre **loopback**: el LLM corre en el
mismo equipo del Edge.

| Método | Ruta | Función | Evidencia | ¿Comprueba el HTTP status? |
|---|---|---|---|---|
| `POST` | `/api/chat` | `Client.Chat` | `ollama/client.go:201` | ✅ sí (`ollama/client.go:212`) |
| `POST` | `/api/show` | `Client.fetchCapabilities` | `ollama/client.go:249` | ❌ **NO** — ver `deuda.md`, D3 |
| `GET` | `/api/tags` | `Client.ListModels` | `ollama/client.go:277` | ❌ **NO** — ver `deuda.md`, D3 |
| `GET` | `/api/tags` | `Client.Health` | `ollama/client.go:318` | ✅ sí (`ollama/client.go:327`) |

### Cuerpo exacto que viaja en `POST /api/chat`

`chatRequestWire`, `ollama/client.go:107-120`. Es **el contrato con Ollama**; cambiar un nombre de
campo aquí rompe la inferencia en campo sin que ningún test unitario lo note.

| Campo JSON | Tipo | Nota |
|---|---|---|
| `model` | string | El modelo lo elige el Edge; **no viaja en el frame del Cloud** (ADR-0045). |
| `messages` | `[{role, content}]` | `role` ∈ `system` / `user` / `assistant`. |
| `stream` | bool | **Siempre `false`** (`ollama/client.go:191`). Sin streaming. |
| `format` | JSON crudo, `omitempty` | `"json"` o un JSON Schema. Fuerza la forma de la salida. |
| `options` | `map[string]any`, `omitempty` | `temperature`, `num_thread`, `num_ctx`, `num_predict`. |
| `think` | `*bool`, `omitempty` | Solo válido en modelos con capability `thinking`. Puntero para poder omitirlo. |
| `keep_alive` | `*int` (SEGUNDOS), `omitempty` | 🔴 **PRIMER NIVEL, no dentro de `options`.** Negativo = para siempre. Viaja como **número**, no como cadena `"5m"`: la cadena pasa por `time.ParseDuration` y un error solo se ve en campo (`ollama/client.go:114-118`). |

### Respuesta que se lee de `POST /api/chat`

`ChatResponse`, `ollama/client.go:123-149`: `model`, `message`, `done`, `total_duration`,
`load_duration`, `prompt_eval_count`, **`prompt_eval_duration`** (el prefill), `eval_count`,
`eval_duration`. Todas las duraciones llegan en **nanosegundos** y `Metrics()` las convierte a ms.

⚠️ **El prefill se mide aparte a propósito.** Publicar un solo número mezcla dos regímenes que se
diferencian en un orden de magnitud: con prefijo **frío** el prefill cuesta ~21,6 ms por token; con
prefijo **caliente** baja a 0,1–1,2 s el prompt entero. Ese número mezclado es el que dejó dos p50
irreconciliables en el ecosistema (~20 s en el informe de diseño contra 8,1 s en campo)
(`ollama/client.go:138-145`).

---

## 3. API pública del paquete `ollama` — el contrato VIVO

| Símbolo | Firma / valor | Evidencia | ¿Lo ejecuta el Edge Agent? |
|---|---|---|---|
| `New` | `func New(baseURL string) *Client` | `ollama/client.go:37` | ✅ `wapp-edge-agent/cmd/agent/cajero.go:123` |
| `Client.Chat` | `(ctx, ChatRequest) (*ChatResponse, error)` | `ollama/client.go:187` | ✅ `wapp-edge-agent/internal/app/cajero/servidor.go:393` |
| `Client.SupportsThinking` | `(ctx, model string) bool` | `ollama/client.go:230` | ✅ `wapp-edge-agent/internal/app/cajero/servidor.go:340` |
| `Client.ListModels` | `(ctx) ([]ModelInfo, error)` | `ollama/client.go:276` | ❌ sin llamante |
| `Client.Health` | `(ctx) error` | `ollama/client.go:317` | ❌ sin llamante no-test |
| `ChatRequest` | struct de 7 campos | `ollama/client.go:73-104` | ✅ |
| `ChatResponse` + `.Content()` + `.Metrics()` | | `ollama/client.go:123-182` | ✅ |
| `Message` | `{Role, Content}` | `ollama/client.go:46` | ✅ `wapp-edge-agent/internal/app/cajero/servidor.go:402` |
| `Metrics` | `{TotalMs, LoadMs, PromptMs, PromptTokens, OutputTokens, TokensPerSec}` | `ollama/client.go:155-167` | ✅ `wapp-edge-agent/internal/app/cajero/inferencia.go:150` |
| `ModelInfo` | `{Name, ParameterSize, SizeBytes}` | `ollama/client.go:269` | ❌ |
| `KeepAliveForever` | `= -1` | `ollama/client.go:54` | ✅ `wapp-edge-agent/internal/app/cajero/cajero.go:346` |
| 🔴 **`DefaultKeepAliveSeconds`** | `= KeepAliveForever` (−1) | `ollama/client.go:63` | ✅ **`wapp-edge-agent/internal/infra/config/config.go:280`** |
| `KeepAliveSeconds` | `func(s int) *int` | `ollama/client.go:68` | ✅ `wapp-edge-agent/cmd/agent/cajero.go:158`, `wapp-edge-agent/internal/app/cajero/cajero.go:730` |

### 🔴 El contrato que más duele romper

`wapp-edge-agent/internal/infra/config/config.go:280` es literalmente:

```go
const DefaultWorkerKeepAliveSeconds = ollama.DefaultKeepAliveSeconds
```

El default del `keep_alive` del Edge **no es un literal del Edge**: lo importa de aquí, porque
—dice el propio comentario del agente (`wapp-edge-agent/internal/infra/config/config.go:266`)— *«lo
recomienda el módulo del proveedor […], que es quien sabe qué significa cada número para Ollama»*.
**Cambiar `ollama/client.go:63` cambia el comportamiento del worker en la caja de cada cliente** en
cuanto se corte el siguiente tag y el consumidor se realinee.

`KeepAliveForever` y `DefaultKeepAliveSeconds` son **dos constantes a propósito**: aquella dice qué
**significa** −1 para Ollama; esta dice qué **elegimos** nosotros, y mañana puede bajar a un valor
finito sin tocar la otra (`ollama/client.go:56-62`).

---

## 4. API pública del paquete `classifier` — huérfana salvo cuatro enteros

| Símbolo | Valor / firma | Evidencia | ¿Lo ejecuta el Edge Agent? |
|---|---|---|---|
| **`DefaultMaxRunes`** | `1000` | `classifier/classifier.go:60` | ✅ solo como constante |
| **`DefaultNumThread`** | `5` | `classifier/classifier.go:67` | ✅ solo como constante |
| **`DefaultNumCtx`** | `4096` | `classifier/classifier.go:97` | ✅ solo como constante |
| **`DefaultNumPredict`** | `256` | `classifier/classifier.go:106` | ✅ solo como constante |
| `New` | `(client llmClient, model string, cfg *intents.Config, opts ...Option) *Classifier` | `classifier/classifier.go:225` | ❌ **retirado el 2026-08-24** |
| `Classifier.Classify` | `(ctx, text) (Classification, error)` | `classifier/classifier.go:264` | ❌ cero llamantes |
| `Classifier.Reload` | `(cfg *intents.Config)` | `classifier/classifier.go:243` | ❌ cero llamantes y **cero disparadores** |
| `FastLane` | `(text string) bool` | `classifier/fastlane.go:33` | ❌ cero llamantes (citado en un comentario, `wapp-edge-agent/internal/app/cola.go:171`) |
| `Option`, `WithMaxRunes`, `WithLLMOptions` | opciones funcionales | `classifier/classifier.go:181-207` | ❌ |
| `Classification`, `Metrics` | structs de salida | `classifier/classifier.go:122-161` | ❌ |

Las cuatro constantes se reexportan en `wapp-edge-agent/internal/app/cajero/cajero.go:75-81`.

⚠️ **`New` recibe una interfaz NO exportada** (`llmClient`, `classifier/classifier.go:111-114`): un
consumidor externo no puede nombrar el tipo para declarar su propio doble. Ver `deuda.md`, D7.

### Forma de `Classification` (lo que devolvería `Classify`)

```go
type Classification struct {
    Intent     string            `json:"intent"`
    Params     map[string]string `json:"params"`
    Confidence float64           `json:"confidence"`
    Metrics    Metrics           `json:"metrics"`   // poblada TAMBIÉN en las salidas degradadas
    Truncado   bool              `json:"truncado"`  // el texto superaba el techo y se recortó
}
```

🔴 **No lleva el texto del cliente** (INV-051.1: el caller loguea la struct entera) **ni texto de
respuesta**: en wApp la respuesta al cliente la produce el Cloud, nunca el LLM del Edge
(`classifier/classifier.go:141-146`).

---

## 5. Contrato de datos que consume: `wapp-shared/intents`

Única dependencia externa (`go.mod:5`, `v0.1.0`). Se consume **sin `replace`**.

| Símbolo | Qué aporta |
|---|---|
| `intents.Config` | `Version`, `UmbralConfianza`, `Intents []Intent`, `Vocabulario []string` |
| `intents.Intent` | `Name`, `Descripcion`, `Params []string`, `Ejemplos []Ejemplo` |
| `intents.Ejemplo` | `Mensaje`, `Params map[string]string` |
| `intents.ReservedUnknown` | `"desconocido"` — el intent reservado; **prohibido declararlo en el contrato** |
| `intents.DefaultThreshold` | `0.6`, umbral por defecto si el contrato no lo declara |

El `Config` **se lo entrega el caller ya deserializado**: este repo no lo lee de disco, ni de red,
ni de base de datos, y no sabe de dónde sale.

---

## 6. rpc, gRPC, CLI

**Ninguno de los tres.** Cero `.proto`, cero cliente/servidor gRPC (`rg -ni 'grpc'` sobre código →
0), cero `func main`, cero `cmd/`, cero comandos. El transporte edge↔cloud vive en
`wapp-cloudlink`; los binarios (`wapp-agent`, `wapp-ctl`) los produce `wapp-edge-agent`.

---

## 7. Variables de entorno

**El código de producción no lee NINGUNA.** `rg -n 'os\.Getenv|LookupEnv'` sobre todo el repo
devuelve **una sola línea**, y es de un test: `classifier/battery_test.go:148`.

### Las que lee este repo (solo con `-tags ollama`)

Nombres **efectivos** — se leen literales con `os.Getenv`, sin loader ni prefijo compuesto.

| Variable | Default | Evidencia |
|---|---|---|
| `WAPP_INTENT_TEST_URL` | `http://127.0.0.1:11434` | `classifier/battery_test.go:26` |
| `WAPP_INTENT_TEST_MODEL` | `qwen3:1.7b` | `classifier/battery_test.go:27` |

### Las del CONSUMIDOR que gobiernan lo que llega aquí

No son de este repo —las lee `wapp-edge-agent`— pero son las que un operador toca para cambiar el
comportamiento de esta librería en campo. Nombres efectivos: el loader compone el prefijo
`WAPP_AGENT_` (`wapp-edge-agent/internal/infra/config/config.go:22`) o `WAPP_WORKER_` según el
bloque.

| Variable efectiva | Default | Qué gobierna aquí |
|---|---|---|
| `WAPP_AGENT_INTENT_OLLAMA_URL` | `http://127.0.0.1:11434` | El `baseURL` que recibe `ollama.New` |
| `WAPP_AGENT_INTENT_MODEL` | `qwen3:1.7b` | El campo `model` de cada `/api/chat` |
| `WAPP_WORKER_NUM_THREAD` | `5` (de `DefaultNumThread`) | `options.num_thread` |
| `WAPP_WORKER_NUM_PREDICT` | `256` (de `DefaultNumPredict`) | `options.num_predict` |
| `WAPP_WORKER_NUM_CTX` | `4096` (de `DefaultNumCtx`) | `options.num_ctx` |
| `WAPP_WORKER_KEEP_ALIVE_SECONDS` | `-1` (de `DefaultKeepAliveSeconds`) | `keep_alive`. 🔴 **Sin guardarraíl `<=0 ⇒ default`, a propósito**: para Ollama negativo / 0 / positivo son tres cosas legítimas y distintas (`wapp-edge-agent/internal/infra/config/config.go:269-274`) |

⚠️ **`OLLAMA_KEEP_ALIVE`** (sin prefijo `WAPP_`) es del **servidor de Ollama**, no de wApp. En el
VPS de UAT existe con valor `-1` en un drop-in de systemd, y eso **tapa** el problema en esa
máquina. En el equipo de un cliente **no hay nadie que la ponga**: por eso el campo viaja en cada
petición (`ollama/client.go:88-92`).

---

## 8. Ficheros que lee y escribe

| Operación | Resultado |
|---|---|
| Ficheros que **escribe** | **Ninguno.** `rg -n 'os\.Create|os\.WriteFile|os\.OpenFile'` → vacío. |
| Ficheros que **lee** en producción | **Ninguno.** |
| Ficheros que lee en test | `classifier/testdata/intents.json` (fixture: 10 intenciones, `umbral_confianza: 0.6`, vocabulario de pizzería) |

La librería es pura: entra texto, sale struct.

---

## 9. Tablas y esquemas que toca

**Ninguno.** Sin base de datos, sin driver SQL, sin migraciones, sin versión de esquema. El
almacén cifrado de `whatsmeow` (y por tanto la **DEK** que lo abre) vive en `wapp-edge-agent` y
**jamás cruza a este módulo**; el `edge.db` que el cajero abría para sondear el contrato de
intenciones **dejó de abrirse** el 2026-08-24, cuando se retiró el clasificador
(`wapp-edge-agent/cmd/agent/cajero.go:118-122`).
