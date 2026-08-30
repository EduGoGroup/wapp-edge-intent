# Deuda viva de `wapp-edge-intent`

Estado: **cero marcadores explícitos**. `rg -n 'TODO|FIXME|HACK|DEUDA|XXX|BUG:'` sobre el repo no
devuelve ni una entrada real (los dos aciertos son prosa, `CHANGELOG.md:139` y
`classifier/classifier.go:35`, ambos «DOS motivos»). Todo lo de abajo se encontró **leyendo**,
no está declarado por nadie, y **ninguna entrada tiene dueño ni ticket**.

Orden: de más peligrosa a más cosmética.

---

## D0 🔴 · CÓDIGO MUERTO VERIFICADO: el paquete `classifier/` entero

**Qué:** 609 líneas de código (`classifier.go` 390 + `prompt.go` 69 + `schema.go` 68 +
`fastlane.go` 43 + `sanitize.go` 39) y **674 de test** (`classifier_test.go`) más 152 de batería,
**sin un solo llamante vivo** desde el 2026-08-24.

**Evidencia (por llamante, no por fichero):**
- Único importador del ecosistema: `wapp-edge-agent/internal/app/cajero/cajero.go:46`.
- Lo que toma de él: **cuatro enteros**, reexportados en
  `wapp-edge-agent/internal/app/cajero/cajero.go:75-81`.
- Llamadas a `Classify(` o `FastLane(` fuera de comentarios: **cero**.
- La retirada, escrita en el consumidor: `wapp-edge-agent/cmd/agent/cajero.go:112-116` y
  `wapp-edge-agent/internal/domain/inbound.go:42`.

**Consecuencia:** se compila, se testea y se lintea en cada gate; sus ~20 tests salen verdes y **no
miden nada de producción**. Ya hubo un precedente en el ecosistema: el ADR-0046 citó
`classifier.go:249` como prueba de que un invariante se cumplía y tuvo que retractarse porque *«esa
evidencia era de CÓDIGO MUERTO»*. Además, todo lo demás de este documento (D1, D4, D5, D7, D8) vive
dentro de este paquete: es deuda que **hoy no muerde y muerde entera el día que alguien lo reviva**.

**Cómo se cierra:** es **una decisión pendiente sin dueño**, no una tarea. Dos salidas, y hay que
elegir una explícitamente:
- **(a) Retirar.** Borrar `classifier/` y **mudar las cuatro constantes** a `ollama/` o al propio
  consumidor. ⚠️ No es un `git rm`: `wapp-edge-agent/internal/app/cajero/cajero.go:60-73` explica
  que se reexportan en el paquete `cajero` —y no en `config`— para no arrastrar `ollama → net/http`
  al grafo de `wapp-ctl`. Mover las constantes sin releer ese razonamiento rompe el binario de
  control.
- **(b) Darle consumidor.** Bajo pull el Cloud arma el prompt; para que este paquete vuelva a tener
  sentido haría falta una decisión de producto que hoy no existe.

🔴 **No lo cierres tú de paso.** Anótalo con dueño y déjalo escrito; retirarlo o revivirlo es
alcance de plan, no de limpieza.

---

## D1 🔴 · `sanitizeParams` corta por **BYTES** en un repo cuyo invariante rector es «por runas»

**Dónde:** `classifier/sanitize.go:31`

```go
if strings.Contains(msg, vl) || (len(vl) >= 4 && strings.Contains(msg, vl[:4])) {
```

`len(vl)` cuenta **bytes** y `vl[:4]` corta **bytes**. El doc comment de encima
(`classifier/sanitize.go:16-17`) promete otra cosa: *«si el valor tiene 4+ **letras**, basta con que
su prefijo de 4 **caracteres** aparezca»*. Y contradice frontalmente el invariante que el mismo
paquete defiende tres ficheros más allá (`classifier/classifier.go:269-270`: *«se recorta por
RUNAS, nunca por bytes»*; `truncateRunes`, `classifier/classifier.go:340-355`).

**No es teórico. Reproducido** ejecutando una copia literal de la función (2026-08-30):

| Caso | Valor del param | Mensaje del cliente | `vl[:4]` | ¿UTF-8 válido? | ¿Sobrevive al allowlist? |
|---|---|---|---|---|---|
| Falso **positivo** CJK | `披萨饼` (3 runas, 9 bytes) | `我要买 披萨 三个` — **no contiene** `披萨饼` | `"披\xe8"` | ❌ | **SÍ** |
| Falso **positivo** CJK | `寿司卷` | `我要 寿司 请` | `"寿\xe5"` | ❌ | **SÍ** |
| Falso **negativo** ES | `café` (4 runas, 5 bytes) | `quiero una cafetera nueva` | `"caf\xc3"` | ❌ | **NO** |

**Consecuencia:** en CJK, «4 caracteres de tolerancia» degeneran a **1,33 caracteres**, así que el
allowlist —cuya única razón de existir es que *«los modelos pequeños copian valores de los ejemplos
del prompt»* (`classifier/sanitize.go:11-14`)— deja pasar exactamente la clase de alucinación que
vino a matar: un valor que el cliente nunca escribió. Y en español, un valor con acento en la 4.ª
posición **pierde** la tolerancia a plural/tipeo justo en el idioma del producto. El fallo es
**silencioso en las dos direcciones**.

**Ningún test lo cubre:** `TestSanitizeParams` (`classifier/classifier_test.go:155`) usa **solo
ASCII**.

**Cómo se cierra:**

```go
r := []rune(vl)
if strings.Contains(msg, vl) || (len(r) >= 4 && strings.Contains(msg, string(r[:4]))) {
```

Y **con test hermano**: los tres casos de la tabla, con el CJK como aserto negativo. Un arreglo sin
el caso CJK deja el invariante igual de indefenso que hoy.

---

## D2 🔴 · Un fallo de red transitorio deshabilita `think:false` **para siempre** en ese proceso

**Dónde:** `ollama/client.go:230-241` (la caché) + `ollama/client.go:244-266` (el fetch).

`fetchCapabilities` devuelve `nil` ante **cualquier** error —cinco `return nil` mudos en
`ollama/client.go:247, 252, 256, 264`— y `SupportsThinking` **cachea ese `nil`**
(`ollama/client.go:236-238`) **sin TTL y sin reintento**. El doc comment lo llama *«la respuesta
segura»* (`ollama/client.go:228-229`), y lo es para **una** llamada; la caché lo vuelve
**permanente**.

**Consecuencia concreta:** si Ollama aún no está listo cuando arranca el Edge —el escenario
habitual: el propio repo documenta cargas en frío de **39 s** (`ollama/client.go:88`)—, queda
grabado `caps["qwen3:1.7b"] = nil` y **todas** las inferencias posteriores de ese proceso se mandan
**sin `think:false`**. En qwen3 eso es justo lo que `classifier/classifier.go:279-280` advierte:
*«la cadena de pensamiento triplica la latencia sin mejorar el resultado de clasificar»*. Un hipo
de red al arrancar se convierte en **latencia ×3 permanente hasta el siguiente reinicio**, en una
pieza cuyo problema declarado número uno es el tiempo.

**Agravante:** `fetchCapabilities` **no comprueba el código de estado**
(`ollama/client.go:254-258`). Un `404` de Ollama con cuerpo JSON decodifica limpiamente en la
struct vacía y se cachea como «no tiene thinking», **indistinguible** de una respuesta legítima.

**Lo que sí hay:** `TestSupportsThinkingFalseOnNetworkError` (`ollama/client_test.go:108`) fija el
comportamiento de **una** llamada. **Nada** fija que el error deba poder reintentarse.

**Cómo se cierra:** separar «no tiene la capability» de «no pude preguntar». Mínimo viable:
`fetchCapabilities` devuelve `([]string, error)` y `SupportsThinking` **no cachea el caso de
error** (o lo cachea con un TTL corto). Test hermano: dos llamadas, la primera con el servidor
caído y la segunda con el servidor vivo, exigiendo que la segunda **sí** consulte.

---

## D3 🟠 · `ListModels` y `fetchCapabilities` no miran el HTTP status; `Chat` y `Health` sí

**Dónde:** `ollama/client.go:281-299` y `ollama/client.go:254-265`.

Asimetría dentro del **mismo fichero**:

| Función | ¿Comprueba status? | Línea |
|---|---|---|
| `Chat` | ✅ | `ollama/client.go:212` |
| `Health` | ✅ | `ollama/client.go:327` |
| `ListModels` | ❌ salta de `ollama/client.go:281` directo al decode en `:297` | — |
| `fetchCapabilities` | ❌ de `ollama/client.go:254` al decode en `:262` | — |

**Consecuencia:** un `500` con cuerpo `{}` hace que `ListModels` devuelva **lista vacía y
`error == nil`** — «no hay modelos instalados» en vez de «Ollama está roto». Que dos funciones del
mismo fichero traten el status y las otras dos no es la clase de invariante que puede depender de
que alguien se acuerde.

**Cómo se cierra:** el mismo `if resp.StatusCode != http.StatusOK` de `Chat` en las otras dos, y
—mejor— un test **estructural** que exija que toda función que haga `c.http.Do` compruebe el
status: si el invariante se repite en N sitios, se vigila la **regla**, no N conductas.

---

## D4 🟡 · `metricsFrom` **tira el prefill**, que es la métrica estrella de `v0.3.0`

**Dónde:** `classifier/classifier.go:131-139`.

`ollama.Metrics` tiene `PromptMs` (`ollama/client.go:163`), pero `classifier.Metrics`
(`classifier/classifier.go:122-128`) **no lo declara**, y `metricsFrom` copia cinco de seis campos.

La ironía: el HEAD del repo (`8ab68d4`) es el commit que **añadió** el prefill, precisamente porque
publicar un solo número *«no es medir, es promediar dos poblaciones distintas»*
(`ollama/client.go:138-145`). El paquete `classifier` sigue promediando. El `CHANGELOG.md:104-108`
lo reconoce por escrito (*«ahí hay un dato que se pierde en la traducción `metricsFrom`»*), lo que
lo convierte en **deuda declarada sin dueño ni ticket**.

**Consecuencia:** inocua hoy (nadie llama al paquete); el día que alguien lo reviva, se encuentra
una métrica que miente por promediado.

**Cómo se cierra:** añadir `PromptMs int64` a `classifier.Metrics` y copiarlo. Es aditivo.

---

## D5 🟡 · `Classify` nunca manda `keep_alive`, la defensa que el propio repo llama imprescindible

**Dónde:** `classifier/classifier.go:287-293` — el `ollama.ChatRequest{…}` no fija `KeepAlive`.

`ChatRequest.KeepAlive` existe desde `v0.3.0` y su doc comment (`ollama/client.go:86-92`) argumenta
que el campo *«sí viaja con cada petición»* mientras que `OLLAMA_KEEP_ALIVE` *«es una propiedad del
servidor de ESA máquina: en el equipo de un cliente no hay nadie que la ponga»*. El clasificador
—el consumidor natural de ese argumento— se queda con el default del servidor (5 min), y al morir
el runner se pierde la **caché de prefijos**.

**Consecuencia:** latente hoy (sin llamantes), trampa mañana.

**Cómo se cierra:** `KeepAlive: ollama.KeepAliveSeconds(ollama.DefaultKeepAliveSeconds)` en el
`ChatRequest`, o mejor un `Option` que lo deje configurable como ya hace el consumidor vivo.

---

## D6 🟡 · Doble trabajo en `SupportsThinking`: el lock se suelta entre el miss y el fetch

**Dónde:** `ollama/client.go:231-239`.

```go
c.mu.Lock(); caps, ok := c.caps[model]; c.mu.Unlock()
if !ok {
    caps = c.fetchCapabilities(ctx, model)   // ← fuera del lock
    c.mu.Lock(); c.caps[model] = caps; c.mu.Unlock()
}
```

**No es un data race** (el mapa siempre va bajo mutex, y `go test -race` pasa), pero N goroutines
que arranquen a la vez con la caché fría lanzan **N peticiones `POST /api/show`** y gana la última.
El aforo de Ollama es de **una plaza** (`wapp-edge-agent/internal/app/cajero/servidor.go:22-23`), así
que la ráfaga compite con la inferencia real.

**Consecuencia:** hoy no muerde porque el agente serializa por su cuenta — es decir, la corrección
es una **propiedad del llamante**, no del cliente. Un segundo consumidor concurrente la pierde sin
aviso.

**Cómo se cierra:** un `singleflight` por modelo, o mantener el lock durante el fetch (más simple,
y el fetch es corto y acotado por `ctx`).

---

## D7 🟡 · `New` recibe una interfaz **no exportada**

**Dónde:** `classifier/classifier.go:225`, con `llmClient` declarada en
`classifier/classifier.go:111-114` (minúscula).

Funciona —pasar `*ollama.Client` compila— pero un consumidor externo **no puede nombrar el tipo**
para declarar su propio doble o una variable intermedia: tiene que redeclarar la interfaz. `go doc`
la muestra como ruido ilegible.

**Consecuencia:** es la clase de detalle que empuja a un consumidor a **copiar en vez de
reutilizar** — exactamente lo que un módulo compartido existe para evitar.

**Cómo se cierra:** exportarla (`type LLMClient interface{…}`) y mantener `llmClient` como alias
interno mientras dure la transición. Es cambio de API pública ⇒ versión menor.

---

## D8 🟢 · Dos errores tragados con fallback silencioso

**Dónde:** `classifier/schema.go:61-66` y `classifier/prompt.go:60-63`.

```go
raw, err := json.Marshal(s)
if err != nil { return json.RawMessage(`{"type":"object"}`) }
```

El comentario lo justifica (*«la entrada es estática: no puede fallar»*) y hoy tiene razón. Pero el
fallback `{"type":"object"}` es **un schema sin `enum`, sin rango de `confidence` y sin
`required`**: si algún día `buildSchema` se vuelve dinámico, ese camino desactiva **de golpe** los
tres invariantes que el fichero entero defiende (`classifier/schema.go:34-52`), **sin log y sin
error**.

Gemelo menor en `classifier/prompt.go:60-63`: un `continue` mudo si un few-shot no serializa. El
prompt sale con menos ejemplos y nadie se entera — cuando el propio fichero dice que *«los ejemplos
valen más que las instrucciones»* (`classifier/prompt.go:14-15`).

**Cómo se cierra:** que la función devuelva error, o que el fallback **entre en pánico** en vez de
degradar en silencio. Un schema inválido servido calladamente es peor que no arrancar — es el mismo
criterio que el ecosistema ya aplica a las plantillas de prompt del Cloud, donde una plantilla
inválida **aborta el arranque** a propósito.

---

## Documentación del propio repo que ya no es cierta

No es código, pero engaña igual y hay que corregirla o retirarla.

| Dónde | Qué afirma | Realidad |
|---|---|---|
| `README.md:5-7` | *«Esa clasificación viaja al cloud como campo nuevo del stream gRPC»* | 🔴 **Falso desde el 2026-08-24.** El campo `intent` está `reserved` en el proto de `wapp-cloudlink`, y **este repo no tiene una sola línea de gRPC**. |
| `README.md:1-8, 30-63` | Presenta `New`/`Classify`/`Reload`/`FastLane` como **el** contrato de la pieza | 🔴 Cero llamantes. El README no menciona en ningún sitio que `ollama/` es hoy lo único vivo. |
| `classifier/classifier.go:262-263` | *«…el camino inline viejo del Edge, que sigue vivo hasta la Ola 3»* | 🔴 La Ola 3 ya ocurrió y ese camino **se borró entero** (`wapp-edge-agent/cmd/agent/cajero.go:50`). El comentario habla en futuro de algo demolido. |
| `classifier/classifier.go:241-242` | `Reload` *«p. ej. cuando el Cloud empuja una config nueva por el stream»* | 🟠 Bajo pull el Cloud arma el prompt y lo baja ya construido; el contrato de intenciones **ya no lo lee este proceso**. `Reload` no tiene ni llamante **ni disparador**. |
| `CHANGELOG.md:110-116` y `:216-220` | *«No se han creado tags ni tocado versiones en ningún `go.mod`»*, y *«el consumidor, hoy en `v0.2.0`»* | 🟠 El tag **`v0.3.0` existe** y apunta a HEAD; el consumidor **ya está realineado** (`wapp-edge-agent/go.mod:8` → `v0.3.0`). Son notas congeladas que nadie actualizó al cortar el release. |
| `README.md:150` y `.gitignore:2` | `make build  # compila` y `/wapp-edge-intent` bajo «Binarios compilados» | 🟡 No hay `cmd/` ni `func main`: `go build ./...` **nunca** ha producido un ejecutable aquí. |

También, aunque viva fuera de este repo y no puedas arreglarlo desde aquí: el `CLAUDE.md` de la
raíz del ecosistema atribuye a esta pieza «señal sellada · `ConfigUpdate` · entitlements». **Ninguna
de las tres está aquí** (`rg -ni 'sello|sealed|grpc|ConfigUpdate|entitlement|nacl|x25519'` sobre el
código → cero). Viven en `wapp-edge-agent` y `wapp-cloudlink`.

---

## Lo que está BIEN y conviene no romper

Para calibrar: este repo está, con diferencia, **mejor documentado por dentro que la media** del
ecosistema. Cada constante lleva su **medición fechada** (`classifier/classifier.go:24-107`); cada
invariante dice de qué fallo de campo nació; `parseClassification` usa una struct `wire` aparte
para que *«ninguna salida del modelo pueda escribir los campos que pone el Edge»*
(`classifier/classifier.go:357-365`, fijado por `TestParseClassificationIgnoresInjectedFields`); y
`truncateRunes` (`classifier/classifier.go:340-355`) tiene un atajo por bytes **correctamente**
razonado. Cero credenciales, cero funciones gigantes (la mayor es `Classify`, 70 líneas y lineal),
ratio test/código ≈ 1:1. La deuda de arriba es real; el listón del repo, también.
