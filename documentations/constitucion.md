# Constitución de `wapp-edge-intent`

Las reglas de esta pieza. Si algo de aquí choca con un comentario del código, gana el código y
este documento está desactualizado: **anótalo, no lo asumas**.

---

## 0. 🔴 El hecho que gobierna todo lo demás

**El paquete `classifier/` no lo llama nadie desde el 2026-08-24.**

| Qué | Evidencia |
|---|---|
| Único importador en todo el ecosistema | `wapp-edge-agent/internal/app/cajero/cajero.go:46` |
| Qué toma de él | **Cuatro enteros**, reexportados en `wapp-edge-agent/internal/app/cajero/cajero.go:75-81` |
| Llamadas a `Classify(` o `FastLane(` fuera de comentarios | **cero** (`rg -n '(classifier\.)?(Classify\|FastLane)\('` sobre `wapp-edge-agent`) |
| Dónde está escrita la causa | `wapp-edge-agent/cmd/agent/cajero.go:112-116` y `wapp-edge-agent/internal/domain/inbound.go:42` |

Al pasar de **push** (el Edge clasificaba por iniciativa propia y adjuntaba la señal al entrante)
a **pull** (el Cloud pide la inferencia y el Edge la sirve) —ADR-0045, ejecutado el 2026-08-24—
el `classifier.New(...)` del agente se retiró entero, y con él su prompt, su schema y el sondeo
del contrato de intenciones en `edge.db`. El prompt lo arma hoy el Cloud y baja ya construido por
el frame `inference_request`.

**Consecuencia para ti, que trabajas aquí:**

1. **`classifier/` son 609 líneas de código y 674 de test sin consumidor vivo.** Se compilan, se
   testean y se lintean en cada gate. Es una **decisión pendiente sin dueño**: o vuelve a usarse, o
   se retira. **No hay plan ni micro-plan que la reclame.** No la des por «previsión»: un objeto sin
   llamante es deuda.
2. **Un test verde de `classifier/` no acredita nada de producción.** Ya hubo un precedente exacto
   en el ecosistema: el ADR-0046 citó `classifier.go:249` como prueba de que un invariante se
   cumplía, y tuvo que retractarse porque *«esa evidencia era de CÓDIGO MUERTO»*.
3. **No arregles nada de `classifier/` «de paso» creyendo que estás arreglando producción.** Lo que
   corre en campo es `ollama/`.
4. **No borres `classifier/` por tu cuenta.** Las cuatro constantes SÍ están vivas y el consumidor
   depende de ellas; retirarlo es una decisión de producto con dueño, no una limpieza.

---

## 1. Invariantes del ecosistema que aplican aquí

Se repiten porque este repo se clona **solo** y quien trabaja aquí puede no tener el resto del
ecosistema al lado. No son adorno: violarlos rompe wApp entero.

### E1 · Zero-knowledge — la nube nunca accede a credenciales ni llaves privadas

Protege **llaves y credenciales**, **no** el contenido de negocio (ese sí sube a la nube, a
propósito). En esta pieza el invariante se cumple **por vacío**: no hay criptografía, ni
credenciales, ni secretos.

- **Cómo se comprueba:** `rg -ni 'nacl|x25519|sealed|SealFor|token|secret|password|apikey' --glob '*.go' .`
  debe devolver **cero** coincidencias en código.
- **Test que lo vigila:** ❌ ninguno. Es una propiedad del repo, sostenida por revisión.
- **Trampa:** si algún día alguien mete aquí una clave de API de proveedor remoto, este repo es
  **público** y la publica al mundo. Este módulo habla **solo** con `127.0.0.1`.

### E2 · Doble llave — la DEK la custodia el cliente, el Lease lo emite y revoca el servidor

La **DEK** descifra el almacén de `whatsmeow` y **jamás cruza ningún contrato**; el **Lease**
autoriza a operar y es el kill-switch anti-clon del servidor.

- **Cómo se comprueba aquí:** ni la DEK ni el Lease aparecen en este repo
  (`rg -ni 'dek|lease' --glob '*.go' .` → cero). El gate del lease vive en el agente
  (`wapp-edge-agent/internal/adapters/cloudlink/inferencia.go:73-78`), **no aquí**.
- **Regla derivada:** este módulo **no debe** aprender a comprobar leases ni a tocar llaves. Es una
  librería sin estado y sin autoridad; el permiso para inferir lo comprueba el llamante.

### E3 · Sin Redis ni broker en el Edge — la concurrencia se resuelve con Go

- **Cómo se comprueba:** `go.mod` tiene **una sola** dependencia externa. Cualquier `require` de
  Redis, RabbitMQ, NATS o similar es una violación directa.
- **Test que lo vigila:** ❌ ninguno (no hay `depguard` configurado en `.golangci.yml`).
- **En esta pieza:** el único mecanismo de concurrencia es `sync.Mutex` sobre la caché de
  capabilities (`ollama/client.go:28`) y `sync.RWMutex` sobre la config del clasificador
  (`classifier/classifier.go:173`).

### E4 · Copia-adaptación, nunca dependencia: prohibido importar un repo `edugo-*`

Se copió código de EduGo y se **adaptó** al espacio de nombres de wApp. Importar `edugo-*` está
prohibido (ADR-0004 del repo de documentación del ecosistema).

- **Cómo se comprueba:** `grep -rn 'edugo-' go.mod go.sum $(find . -name '*.go')` → debe dar cero.
  ⚠️ Ojo con el falso positivo: el **namespace de GitHub** es `EduGoGroup`, y `EduGoGroup/wapp-*`
  es legítimo. Lo prohibido es `edugo-api-*`, `edugo-shared`, `edugo-ui-*`, `edugo-worker`.
- **Test que lo vigila:** ❌ ninguno.

### E5 · El código compartido interno vive en `wapp-shared`, con releases por módulo

`shared/wapp-shared` es un **monorepo multi-módulo propio de wApp** con tags `<modulo>/vX.Y.Z`.
Es **distinto** de `edugo-shared`, que no se importa nunca.

- **Aquí:** la única dependencia externa es `github.com/EduGoGroup/wapp-shared/intents v0.1.0`
  (`go.mod:5`), consumida **como dependencia normal, sin `replace`**.
- **Cómo se comprueba:** `go.mod` no tiene bloque `replace`. El overlay local `go.work` está en
  `.gitignore:17-19` a propósito: es solo de desarrollo, **no se commitea**.
- **Trampa:** un puerto contra `wapp-shared` se verifica contra el **tag publicado**, no contra el
  árbol de al lado. Si trabajas con `go.work` apuntando a un `wapp-shared` sin publicar, compila
  aquí y **rompe en el consumidor**.

---

## 2. Invariantes propios de esta pieza

### P1 🔴 · Se corta **por RUNAS, jamás por bytes**

Cortar bytes parte un carácter multi-byte por la mitad y manda UTF-8 inválido al modelo.

- **Dónde vive:** `classifier/truncateRunes` (`classifier/classifier.go:340-355`), con su atajo por
  bytes correctamente razonado (`len(s) <= limit` es cota superior segura del número de runas).
- **Test que lo vigila:** ✅ `TestTruncateRunesCutsByRunesNotBytes` (`classifier/classifier_test.go:308`),
  `TestClassifyTruncatesInputAndMarksTruncado` (`:341`).
- 🔴 **EL INVARIANTE ESTÁ ROTO EN `sanitizeParams`** (`classifier/sanitize.go:31`, `vl[:4]`), y el
  fallo está **reproducido**. Ver `deuda.md`, D1. No copies ese patrón.

### P2 · El LLM extrae, el código resuelve

La gramática (JSON Schema) fuerza la **forma**; la **semántica** la valida código determinista.

- **Dónde vive:** `classifier/schema.go:21-60` (forma) y `classifier/sanitize.go:19-38` (semántica).
- **Por qué:** los modelos pequeños copian valores de los ejemplos del prompt. Medido: alucinaron
  un número de pedido que el cliente nunca escribió (`classifier/sanitize.go:11-14`).
- **Test:** ✅ `TestSanitizeParams` (`classifier/classifier_test.go:155`) — pero **solo con ASCII**.

### P3 · Las claves de `params` van **libres**, nunca declaradas como `properties`

- **Dónde vive:** `classifier/schema.go:54-57` — `additionalProperties: {"type":"string"}`.
- **Por qué:** la gramática de llama.cpp exige las propiedades **en el orden declarado**. Si el
  modelo emite primero una clave posterior, las anteriores quedan prohibidas, y qwen3 acababa
  metiendo la cantidad bajo otra clave: el «3» se perdía en silencio. No alucinó, **lo acorraló la
  gramática** (`classifier/schema.go:13-20`).
- **Test:** ✅ `TestBuildSchemaParamsAreFreeNotProperties` (`classifier/classifier_test.go:63`), y el
  canario de campo `canario_gramatica` (`classifier/battery_test.go:68`), que repite tres veces
  `"quiero 3 pizzas de pepperoni"` y exige `cantidad == "3"`.

### P4 · `confidence` va acotada a `[0,1]` **en la gramática**, no en el parseo

Sin `minimum`/`maximum`, `{"confidence": 100}` es JSON legal y el umbral de `Classify`
(`if out.Confidence < cfg.UmbralConfianza`) queda **decorativo**: `100 < 0.6` es falso.

- **Medido:** ante una entrada que desbordaba la ventana, qwen3:1.7b devolvió `horario_atencion`
  con `confidence: 100` y el umbral (0.6) la dejó pasar (`classifier/schema.go:34-48`).
- **Dónde vive:** `classifier/schema.go:53`.
- **Test:** ✅ `TestBuildSchemaBoundsConfidence` (`classifier/classifier_test.go:107`) y
  `TestClassifyAppliesThresholdAndSanitize` (`:228`).
- 🔴 **Lo que NO promete:** `parseClassification` **no satura ni rechaza** un valor fuera de rango.
  Si algún día se usa el clasificador **sin** `format`, hace falta saturar en Go
  (`classifier/schema.go:50-52`).

### P5 · Ninguna salida del modelo puede escribir campos que pone el Edge

`Metrics` y `Truncado` los pone el código. Por eso se decodifica en un tipo **aparte**
(`classificationWire`, `classifier/classifier.go:361-365`) y no directamente en `Classification`.

- **Test:** ✅ `TestParseClassificationIgnoresInjectedFields` (`classifier/classifier_test.go:652`).

### P6 · `Classification` **nunca** lleva el texto del cliente (INV-051.1)

El caller loguea la struct entera; si llevara el texto, el log del Edge tendría contenido del
cliente. Tampoco lleva texto de respuesta: **la respuesta al cliente la produce el Cloud, jamás el
LLM del Edge** (`classifier/classifier.go:141-146`).

- **Cómo se comprueba:** los campos de `Classification` son `Intent`, `Params`, `Confidence`,
  `Metrics`, `Truncado`. Añadir un campo con el texto rompe el invariante.
- **Test que lo vigila:** ❌ ninguno directo. Es estructural y depende de revisión.

### P7 · El saneo se hace contra el texto **truncado**, no contra el original

El invariante real es «el valor estaba en lo que el modelo **leyó**». Sanear contra el texto
completo relajaría el allowlist justo en las entradas más sospechosas, las gigantes
(`classifier/classifier.go:311-319`).

- **Test:** ✅ `TestClassifySanitizesAgainstTheTruncatedText` (`classifier/classifier_test.go:455`).

### P8 · `keep_alive` es campo de **primer nivel** de `/api/chat`, nunca va dentro de `options`

Metido en `options`, Ollama lo **ignora en silencio** (las claves desconocidas de `options` no dan
error) y el runner se muere a los 5 minutos llevándose la caché de prefijos, sin que nada lo
delate (`ollama/client.go:99-102`).

- **Dónde vive:** `chatRequestWire.KeepAlive` (`ollama/client.go:119`), serializado como
  `keep_alive` al mismo nivel que `model` y `messages`.
- **Test:** ✅ `TestChatKeepAliveEnElWire` (`ollama/client_test.go:220`), con tabla de casos.

### P9 · `ChatRequest.KeepAlive` es **puntero**, y tiene que serlo

Con un `int` desnudo el valor **cero** —que para Ollama significa «descarga el modelo AHORA
MISMO»— sería indistinguible de «no lo fijé». `omitempty` sobre puntero omite solo cuando es `nil`;
un puntero a 0 **sí** se serializa (`ollama/client.go:94-97`).

- **Regla derivada:** en el consumidor **no hay** guardarraíl `<=0 ⇒ default` para este campo, al
  revés que en sus vecinos, y eso es deliberado
  (`wapp-edge-agent/internal/infra/config/config.go:269-274`).

### P10 · El `http.Client` **no tiene timeout global**, a propósito

El plazo lo pone el `context` de cada llamada. Una carga en frío puede tardar 3–7 s (y se han
medido **39 s** en campo). Poner un `Timeout` en `http.Client{}` mataría inferencias legítimas
(`ollama/client.go:33-36`).

- **Cómo se comprueba:** `ollama/client.go:40` construye `&http.Client{}` sin campos.
- **Test:** ✅ `TestChatHonorsContextCancellation` (`ollama/client_test.go:73`).

### P11 · La ventana de contexto **no se sube** para arreglar una entrada grande

El problema que aparece antes es el **TIEMPO**, no el contexto. Subir `num_ctx` a 8192 cuesta
**+493 MB de RSS** permanentes en la caja del cliente (la KV cache crece lineal) y no compra nada.
La respuesta correcta a una entrada gigante es **bajar el techo de entrada**
(`classifier/classifier.go:81-86`).

- **Test:** ✅ `TestDefaultCeilingFitsTheContextWindow` (`classifier/classifier_test.go:413`).

### P12 · Los cuatro números del modelo se cambian **midiendo**, no opinando

`DefaultMaxRunes=1000`, `DefaultNumThread=5`, `DefaultNumCtx=4096`, `DefaultNumPredict=256`
(`classifier/classifier.go:60,67,97,106`). Cada uno lleva su medición **fechada** en su doc
comment, y varios corrigen números que el diseño y un ADR habían puesto a ojo (el `num_thread`
era «2» en el ADR-0038 §3 y quedó corregido a 5 por la medición sobre el VPS AMD real).

- **Se exportan EXPRESAMENTE para el consumidor**, para que no duplique los números a ciegas
  (`classifier/classifier.go:22-23`).

---

## 3. Tecnología y versiones reales

Todo sacado de `go.mod`, `Makefile` y `.github/workflows/ci.yml`, no de prosa.

| Qué | Valor | Evidencia |
|---|---|---|
| Módulo | `github.com/EduGoGroup/wapp-edge-intent` | `go.mod:1` |
| Go | **`1.26.5`** (versión de parche completa, no `1.26`) | `go.mod:3` |
| Toolchain del CI | `GO_VERSION: '1.26.5'` | `.github/workflows/ci.yml:15` |
| Toolchain del Makefile | `GO_VERSION := 1.26.5` | `Makefile:4` |
| Directiva `toolchain` | **no hay** | `go.mod` |
| Dependencia externa | **UNA**: `github.com/EduGoGroup/wapp-shared/intents v0.1.0` | `go.mod:5` |
| `replace` | **ninguno** | `go.mod` |
| Linter | `golangci-lint v2.12.2`, config `version: "2"` | `Makefile:5`, `.golangci.yml:2` |
| Modelo de referencia | `qwen3:1.7b` | `classifier/battery_test.go:26` |
| Ollama en UAT | `0.32.6`, un único modelo cargado (`qwen3:1.7b`, 1,9 GB, 100 % CPU, `context 4096`) | verificación de campo 2026-08-30 |

**Lo que NO hay, y es importante que sigas sin meterlo:** base de datos, migraciones, ficheros en
disco, frontend, plantillas, gRPC, criptografía, `ConfigUpdate`, entitlements, binario.
`go.sum` lista `testify`, `yaml.v3` y `go-spew`, pero vienen del grafo de módulos de
`wapp-shared/intents`: **ningún test de este repo importa testify** — se usa `testing` a secas.

⚠️ **Corrección a la documentación del ecosistema:** el `CLAUDE.md` de la raíz `wApp` atribuye a
esta pieza «señal sellada · `ConfigUpdate` · entitlements». **Ninguna de las tres está aquí**;
viven en `wapp-edge-agent` y `wapp-cloudlink`. No las busques ni las añadas.

---

## 4. Convenciones de código

- **Comentarios en español**, densos, y con la **medición fechada** que justifica cada número.
  Es el estándar de este repo y está por encima de la media del ecosistema: mantenlo. Si cambias
  una constante sin actualizar su doc comment, dejas una mentira firmada.
- **Un fichero por responsabilidad** dentro de `classifier/`: `classifier.go` (tipo y flujo),
  `prompt.go`, `schema.go`, `sanitize.go`, `fastlane.go`. No metas lógica nueva en `classifier.go`
  si tiene su sitio.
- **Ningún error se traga sin comentario que lo justifique.** Hoy se cumple; los dos casos que
  existen (`classifier/schema.go:61-66` y `classifier/prompt.go:60-63`) llevan su razón escrita —
  y aun así son deuda (ver `deuda.md`, D8).
- **Opciones funcionales** para configurar (`Option`, `WithMaxRunes`, `WithLLMOptions`), y
  `WithLLMOptions` **fusiona**, no reemplaza: la temperatura 0.1 sobrevive a cualquier fusión que
  no la nombre (`classifier/classifier.go:195-207`).
- **Tipos `wire` separados** para lo que cruza la frontera JSON (`chatRequestWire`,
  `classificationWire`). No decodifiques directamente en el tipo público.
- **Cero `TODO`/`FIXME` en el repo.** Verificado. Si necesitas dejar uno, mejor abre una entrada en
  `deuda.md` con `fichero:línea`.
- El código nuevo **no puede leer variables de entorno**: hoy el código de producción no lee
  **ninguna** (`rg -n 'os\.Getenv|LookupEnv'` solo toca `classifier/battery_test.go:148`). La
  configuración la inyecta el llamante por parámetro.

---

## 5. Trampas conocidas — lo que un agente hace mal aquí si nadie se lo dice

1. **Creer que `classifier/` es el producto.** El `README.md` del repo lo presenta como tal y
   **está desactualizado desde el 2026-08-24**. Lo vivo es `ollama/`. Ver §0.
2. **Creer al `README.md` del repo sobre la arquitectura.** Dice que la clasificación *«viaja al
   cloud como campo nuevo del stream gRPC»* (`README.md:5-7`): **falso**. El campo `intent` está
   `reserved` en el proto desde el 2026-08-24, y este repo **no tiene una sola línea de gRPC**.
3. **Copiar el patrón de `sanitize.go:31` (`vl[:4]`).** Corta **bytes** en un repo cuyo invariante
   rector es cortar por **runas**. Está roto y reproducido: ver `deuda.md`, D1.
4. **Tocar `ollama/client.go:63` (`DefaultKeepAliveSeconds`) creyendo que es un detalle interno.**
   Es el **default del `keep_alive` del Edge entero**: `wapp-edge-agent/internal/infra/config/config.go:280`
   lo importa en vez de escribir el número. Cambiarlo cambia el comportamiento del worker en la
   caja de cada cliente en la siguiente release.
5. **Poner un `Timeout` en el `http.Client`.** Ver P10. Mata inferencias legítimas en frío.
6. **Meter `keep_alive` dentro de `options`.** Ver P8. Falla **en silencio**.
7. **Añadir `properties` a `params` en el schema.** Ver P3. El «3» se pierde en silencio.
8. **Subir `num_ctx` para «que quepa más».** Ver P11. Cuesta RAM permanente y no arregla el
   problema real, que es el tiempo.
9. **Dar por probada la batería.** `TestBattery` vive tras `//go:build ollama`
   (`classifier/battery_test.go:1`): **sin el tag ni se compila**. Antes no tenía tag y se saltaba
   sola en cada CI, «dando la falsa sensación de estar cubriendo algo»
   (`classifier/battery_test.go:17-21`). Con el tag, la ausencia de Ollama es `t.Fatalf`, no
   `t.Skipf` (`classifier/battery_test.go:33`). **Cuenta siempre los SKIP** — ver `operacion.md`.
10. **Creer que un PR valida algo.** `ci.yml` es `workflow_dispatch` (`.github/workflows/ci.yml:11-12`):
    no se dispara ni con push ni con PR. El gate real es local. Ver `operacion.md`.
11. **Commitear `go.work`.** Está en `.gitignore:17-19` a propósito: es un overlay solo-local.
12. **Buscar un binario.** No hay `cmd/`, no hay `func main`. `make build` compila librerías y no
    deja artefacto, pese a que `.gitignore:2` reserva `/wapp-edge-intent` como «binario compilado».
13. **Editar `CHANGELOG.md` asumiendo que sus «Notas de release» son verdad.** Las de `v0.3.0` y
    `v0.2.0` afirman que no se cortaron tags: **el tag `v0.3.0` existe** y apunta a HEAD, y el
    consumidor ya está realineado (`wapp-edge-agent/go.mod:8` → `v0.3.0`). Ver `deuda.md`.
