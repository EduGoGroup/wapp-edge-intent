# Changelog

Todos los cambios notables de `wapp-edge-intent`. El formato sigue
[Keep a Changelog](https://keepachangelog.com/es-ES/1.1.0/) y el versionado es
[SemVer](https://semver.org/lang/es/).

## [Unreleased] — propuesta: **v0.2.0**

Versión **menor**, no parche: hay **API pública nueva**. Todo lo de abajo es
**aditivo** y **no rompe a ningún consumidor de `v0.1.0`** — `New` pasa a
variádico (`opts ...Option`), que es compatible hacia atrás para todos los
llamadores existentes, y `Classification` solo gana campos. Un consumidor que
compile contra `v0.1.0` compila igual contra esto sin tocar una línea.

Plan 051 (worker cajero del Edge), Ola 2 · tareas **T2.5** y **T2.6**.

### Added

- **Techo de entrada por runas** (`T2.5`). `Classify` recorta el texto a
  `DefaultMaxRunes` (**4000**) antes de mandarlo al modelo. El corte es por
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
