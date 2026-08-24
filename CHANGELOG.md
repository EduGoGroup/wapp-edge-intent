# Changelog

Todos los cambios notables de `wapp-edge-intent`. El formato sigue
[Keep a Changelog](https://keepachangelog.com/es-ES/1.1.0/) y el versionado es
[SemVer](https://semver.org/lang/es/).

## [Unreleased]

Nada todavía.

## [0.3.0] — 2026-08-24

Versión **menor**: API pública nueva y **estrictamente aditiva**. `ChatRequest`
gana un campo opcional, `ChatResponse` y `Metrics` ganan uno cada uno, y aparecen
dos constantes y una función. Un consumidor de `v0.2.0` compila sin tocar una
línea y —si no fija nada— **manda por el cable exactamente el mismo cuerpo que
antes**, byte por byte. Verificado compilando el uso real del consumidor
(`wapp-edge-agent`) contra esta copia: su literal `ollama.ChatRequest{…}` lleva
claves, así que un campo nuevo no lo rompe.

Plan 044 (carrito LLM), Ola 1.7 · tareas **T1.7-4** y **T1.7-5**. Las dos existen
para que el Edge Agent pueda hacer su mitad: sin este release no hay dato que
propagar ni `keep_alive` que mandar.

### Added

- **`ChatRequest.KeepAlive`** (`*int`, segundos) y su clave `keep_alive` en el
  cuerpo de `/api/chat` (`T1.7-4`). Con `nil` —el default— **no se manda la
  clave** y decide el servidor (5 min, o lo que diga `OLLAMA_KEEP_ALIVE`).
  - **Por qué**: cuando el runner de Ollama muere por silencio no se lleva solo
    el modelo, se lleva **la caché de prefijos** con él. El siguiente mensaje
    paga carga del modelo (**39 s medidos** el 2026-08-23) **más** el prefill en
    frío del prompt entero. En el VPS de UAT eso lo tapa hoy
    `OLLAMA_KEEP_ALIVE=-1` en el env de la unidad, pero eso es una propiedad de
    **esa** máquina: en el equipo de un cliente no hay quien la ponga, y el campo
    sí viaja con cada petición.
  - **Es puntero, no `int`**: para Ollama el cero significa «descarga el modelo
    en cuanto respondas», que es lo contrario de lo que queremos. Con un `int`
    desnudo ese cero sería indistinguible de «no lo fijé»; con puntero +
    `omitempty` se distinguen, porque `omitempty` sobre un puntero omite solo
    cuando es `nil` (un puntero a `0` sí se serializa).
  - **Viaja como número (segundos), no como la cadena `"5m"`** que Ollama también
    acepta: el número lo lee directo —cualquier negativo es «para siempre»—
    mientras que la cadena pasa por `time.ParseDuration`, y una cadena mal
    escrita es un `400` que solo aparece en campo. Además el valor nace de una
    configuración, donde un entero es lo natural.
  - **No va dentro de `Options`**: `keep_alive` es un campo de primer nivel de
    `/api/chat`. Metido en `options`, Ollama lo **ignora en silencio** —las
    claves desconocidas de `options` no dan error— y el modelo seguiría
    muriéndose sin que nada lo delatara.
- **`KeepAliveForever` (`-1`)**, la forma canónica de «para siempre», y
  **`DefaultKeepAliveSeconds`**, el valor que este módulo recomienda al Edge (hoy
  el mismo `-1`). Son dos constantes a propósito: la primera dice qué
  **significa** `-1` para Ollama, la segunda qué **elegimos** nosotros, y mañana
  la política puede bajar a un valor finito sin tocar el significado.
- **`KeepAliveSeconds(s int) *int`**: envuelve un entero para poder asignarlo al
  campo. Go no deja tomar la dirección de un literal, y sin esto cada llamador
  necesitaría una variable temporal.
- **`ChatResponse.PromptEvalDuration`** (`int64`, ns, clave
  `prompt_eval_duration`) y su derivado **`Metrics.PromptMs`** (`T1.7-5`). Es el
  **prefill**: lo que cuesta digerir el prompt de entrada antes de generar el
  primer token de salida — el gemelo de `EvalDuration`, que mide la
  **generación**, y ninguno de los dos incluye `LoadDuration` (cargar el modelo).
  - **Por qué**: Ollama **siempre lo ha devuelto y nosotros lo tirábamos**. Por
    eso la latencia se publicaba como **un solo número que mezcla dos regímenes
    separados por un orden de magnitud**: con el prefijo **frío** el prefill
    cuesta ~**21,6 ms por token**; con el prefijo **caliente** baja a **0,1–1,2 s
    el prompt entero**. Ese número mezclado es el que dejó **dos p50
    irreconciliables** en el repo —~20 s en el informe de diseño contra **8,1 s**
    en campo—: la diferencia no era el modelo ni la máquina, era **el calor del
    prefijo**.
  - `PromptMs` se añade a `Metrics` y no solo a la respuesta cruda porque
    `Metrics` es **la** vista que consume el Edge; dejarlo fuera obligaría a que
    el mismo log sumara números de dos sitios y en dos unidades.

### Notas de release

- **No** se han creado tags ni tocado versiones en ningún `go.mod`. El corte de
  `v0.3.0` y la realineación del consumidor (`wapp-edge-agent`, hoy en `v0.2.0`)
  van por el flujo de release del ecosistema.
- `classifier.Metrics` (el tipo público del paquete que hoy **nadie llama**;
  el Edge solo le importa cuatro constantes) **no** gana el prefill: tocarlo
  quedaba fuera del alcance de la ola. Si ese paquete volviera a la vida, ahí hay
  un dato que se pierde en la traducción `metricsFrom`.

## [0.2.0] — 2026-08-16

Versión **menor**, no parche: hay **API pública nueva**. La API es **compatible
hacia atrás** — `New` pasa a variádico (`opts ...Option`), que compila igual para
todos los llamadores existentes, y `Classification` solo gana campos. Un consumidor
de `v0.1.0` compila contra esto sin tocar una línea.

⚠️ Pero **no todo es aditivo**: `DefaultMaxRunes` baja de **4000 a 1000** runas.
Compila igual y **cambia el comportamiento en tiempo de ejecución** — una entrada de
entre 1.000 y 4.000 runas que antes viajaba entera al modelo ahora se recorta y llega
con `Truncado: true`. Está en `Changed`, con la medición que lo obliga.

Plan 051 (worker cajero del Edge), Ola 2 · tareas **T2.5** y **T2.6**.

### Added

- **Techo de entrada por runas** (`T2.5`). `Classify` recorta el texto a
  `DefaultMaxRunes` (**1000**; ver el `Changed` de abajo) antes de mandarlo al modelo. El corte es por
  **runas**, nunca por bytes: jamás parte un carácter multi-byte. Configurable con
  `WithMaxRunes(n)`; con `n <= 0` cae al default (nunca «sin techo»).
  - **Por qué**: sin techo, pegar ~65 KB de texto en un chat basta para que la
    inferencia tarde lo suficiente como para contar fallos y **abrir el circuit
    breaker compartido** del Edge, apagando el clasificador de **todas** las
    sesiones. Era la denegación de servicio más barata del ecosistema.
  - Se aplica **dentro de `Classify`**, no en el caller: protege por igual al worker
    cajero (que concatena un lote y llama una vez) y al camino inline viejo del Edge,
    que sigue vivo hasta la Ola 3. Ningún llamador puede olvidárselo.
- **Opciones de modelo explícitas** (`T2.5`): además de `temperature: 0.1`, cada
  inferencia manda ahora `num_thread`, `num_ctx` y `num_predict`. Constantes
  exportadas para que el caller no duplique los números a ciegas:
  `DefaultNumThread = 5` (medición de la O0 sobre el VPS AMD real),
  `DefaultNumCtx = 4096` (cabe prompt de sistema + techo de entrada + respuesta; y
  hace de segundo techo de costo), `DefaultNumPredict = 256` (la salida son 30–60
  tokens medidos; acota una generación desbocada).
- `WithLLMOptions(map[string]any)`: **fusiona** sobre las opciones por defecto sin
  borrarlas. La **temperatura 0.1 sobrevive** a cualquier fusión que no la nombre;
  una clave que el caller sí nombra gana como override deliberado. El mapa recibido
  se copia.
- `Option` y el patrón `New(client, model, cfg, opts...)`.
- **`Classification.Metrics`** (`T2.6`): el costo de la inferencia
  (`total_ms`, `load_ms`, tokens de prompt y de salida, tokens/s) que
  `ollama.ChatResponse.Metrics()` **ya calculaba y `Classify` tiraba**. Viene
  poblada también en las salidas degradadas (JSON ilegible, bajo umbral): son justo
  los casos que hay que medir. Se declara el tipo `classifier.Metrics` para que el
  consumidor no tenga que importar el paquete `ollama`.
- **`Classification.Truncado`** (`bool`): observable a propósito, el Edge lo loguea.
  Un `Truncado` crónico significa techo mal calibrado para ese tenant — o alguien
  probando a tumbar el clasificador.

### Changed

- ⚠️ **CAMBIO DE COMPORTAMIENTO — `DefaultMaxRunes` baja de `4000` a `1000` runas.**
  El 4000 se calculó a ojo contando tokens de español y **no sobrevivió a la medición
  contra Ollama real**. Falla por dos motivos independientes:
  - **Encaje.** El prompt de sistema mide **911 tokens** y la densidad depende del
    alfabeto (tok/runa medidos con `qwen3:1.7b`): español **0,257**, cirílico
    **0,390**, CJK **0,666**, emoji **1,000**. Con techo 4000 el `prompt_eval_count`
    real es 1.914 (es), 2.472 (ru), 3.577 (CJK) y **4.912 (emoji)**. El emoji **no
    cabe** en `DefaultNumCtx` (4096): medido, Ollama evaluó solo 2.050 tokens y
    **descartó el 58 % de la entrada**.
  - **Tiempo**, que ninguna ventana arregla y es el peor de los dos. A 4000 runas la
    inferencia tarda **32,6 s en español**, 34–71 s en CJK y **119,7 s en emoji**. El
    plazo del worker cajero (15 s) ya se cruza a **~1.500 runas de español** y **~500
    de CJK**, mucho antes de que el contexto se quede corto.
  - **Por qué 1000**: en el peor caso concebible (1,0 tok/runa) son 1.000 tokens; más
    el prompt de sistema de un «contrato rico» (~1.500) y la respuesta (256) suman
    **2.756 < 4.096**. Cabe siempre, en cualquier alfabeto y aunque el contrato
    engorde; acota la latencia del peor caso a ~13 s (es) y ~26 s (CJK); y sigue
    siendo **5× el lote real más grande observado** (8 mensajes concatenados, 98–196
    runas), así que no toca tráfico legítimo. Fijado por
    `TestDefaultCeilingFitsTheContextWindow`, que comprueba la **aritmética**, no el
    número.
  - **`DefaultNumCtx` y `DefaultNumPredict` NO se tocan.** Subir `num_ctx` a 8192
    arregla el encaje, cuesta **+493 MB de RSS medidos** y no toca el problema del
    tiempo. La respuesta correcta a una entrada gigante es bajar el techo de entrada,
    no ensanchar la ventana.
  - **Impacto para el consumidor**: el worker cajero del Edge toma este default vía
    `WAPP_WORKER_MAX_RUNES` / `worker.max_runes`; quien haya fijado un valor propio en
    su config lo conserva.
- **`sanitizeParams` se aplica ahora contra el texto TRUNCADO**, no contra el
  original. El invariante que protege el allowlist es «el valor estaba en lo que el
  modelo **leyó**»: un valor que solo aparece en la cola cortada no pudo salir de
  ahí, así que el modelo lo alucinó (o lo copió de los ejemplos del prompt) y
  coincidió por casualidad — exactamente la clase de fallo que `sanitizeParams`
  existe para matar. Sanear contra el texto completo relajaría el allowlist justo en
  las entradas más sospechosas. No se pierde ningún param legítimo: uno legítimo,
  por construcción, vive en lo que el modelo vio. Queda fijado por
  `TestClassifySanitizesAgainstTheTruncatedText`.
- La salida del modelo se decodifica en una struct interna (`classificationWire`) en
  vez de directamente en `Classification`: los campos que pone el Edge (`Metrics`,
  `Truncado`) no deben ser escribibles desde el JSON que devuelve el LLM.
- **`classifier/battery_test.go` pasa a `//go:build ollama`** y su comprobación de
  salud cambia de `t.Skipf` a `t.Fatalf`. Antes no tenía build tag: corría en cada
  CI y **se saltaba en cada CI**, en silencio, aparentando una cobertura que no
  existía. Ahora pedir el tag es pedir la batería, y no tener Ollama es un fallo.
  `make battery` pasa `-tags ollama`; `.golangci.yml` declara el tag para que el
  fichero siga lintándose y no se pudra.

### Fixed

- **El umbral de confianza no podía atrapar nada.** El schema declaraba
  `"confidence": {"type":"number"}` **sin `minimum` ni `maximum`**, así que
  `{"confidence": 100}` era JSON perfectamente válido para la gramática y el filtro de
  `Classify` (`if out.Confidence < cfg.UmbralConfianza`) resultaba **decorativo**:
  `100 < 0.6` es falso y la respuesta pasaba entera.
  - **Medido**, no teórico: ante una entrada que desbordaba la ventana de contexto,
    `qwen3:1.7b` devolvió `horario_atencion` con `confidence: 100` y el umbral (0.6)
    la dejó pasar — un intent **seguro y equivocado**.
  - Afecta a **cualquier** salida en la que el modelo confunda la escala (0–1 vs
    porcentaje), no solo al desbordamiento.
  - **Arreglo**: `confidence` se acota a `[0,1]` en el schema, de modo que la gramática
    impide emitir el número fuera de escala y el umbral vuelve a comparar lo que cree
    comparar. Lo que **no** promete: el modelo sigue pudiendo emitir un `1.0` seguro y
    equivocado. La defensa es la gramática — `parseClassification` no satura ni rechaza
    un valor fuera de rango. Fijado por `TestBuildSchemaBoundsConfidence`.
- **Corregidos dos comentarios que decían algo falso** sobre `DefaultNumCtx`: (1)
  nombraban el **CJK** como el alfabeto que podía desbordar la ventana, cuando el CJK
  es justo el que cabe (3.577 de 4.096) y quien desborda es el **emoji**; y (2)
  prometían que un desbordamiento «degrada a `desconocido` en silencio», cuando lo
  medido es que **no degrada**: devuelve un intent plausible y equivocado con confianza
  máxima. Desbordar es un fallo silencioso y **activo**, no una degradación segura.

### Docs

- `README.md`: sección de API pública con la tabla de constantes y su porqué;
  invariantes nuevos (techo, saneo contra el texto truncado, opciones explícitas,
  métricas); batería documentada con su build tag. Corregida la afirmación obsoleta
  de que `wapp-shared/intents` se consume por `replace` — se consume como
  dependencia normal (`intents v0.1.0`).

### Notas de release

- **No** se han creado tags ni tocado versiones en ningún `go.mod`. El corte de
  `v0.2.0` y la realineación del consumidor (`wapp-edge-agent`, hoy en `v0.1.0`) van
  por el flujo de release del ecosistema.

## [0.1.0]

- Primera versión publicada: cliente de Ollama (`Chat` con structured outputs,
  caché de capabilities, `ListModels`, `Health`, `Metrics`) y clasificador de
  intenciones (`New`/`Reload`/`Classify`, prompt y JSON Schema regenerados desde el
  contrato `wapp-shared/intents`, `sanitizeParams`, `FastLane`, umbral de
  confianza). ADR-0020.
