# wapp-edge-intent

Clasificador de intenciones **local al Edge** del ecosistema wApp: lee el mensaje
que un cliente escribe por WhatsApp y produce `{intent, params, confidence}` con un
LLM pequeño corriendo en la caja del cliente (Ollama, `qwen3:1.7b`). Esa
clasificación viaja al cloud como campo nuevo del stream gRPC; el cloud decide el
flujo. El JSON solo existe entre el binario del Edge y Ollama (localhost).

Es la **pieza 06** del ecosistema (ver `docs/piezas/06` en el repo raíz `wApp`) y
materializa **ADR-0020** (LLM en el Edge como pre-clasificador). El costo de
inferencia queda en el hardware del cliente; el cloud casi no cambia.

Principio rector: **el LLM extrae, el código resuelve**. El modelo solo produce la
forma (forzada por JSON Schema); el allowlist de params y las decisiones de negocio
son código determinista. Si Ollama falla o tarda, el Edge envía el mensaje **sin
clasificar** (degradación, nunca caída): el LLM solo suma.

## Estructura

```
ollama/       cliente HTTP de Ollama (Chat con structured outputs, métricas,
              caché de capabilities, ListModels, Health). Sin timeout global:
              lo pone el contexto del caller.
classifier/   Classifier: New(client, model, cfg, opts...) + Reload(cfg) +
              Classify(ctx, text). prompt/schema regenerados desde el contrato de
              intents; sanitizeParams como allowlist real; FastLane para descartar
              lo trivial sin LLM.
```

El contrato de intenciones lo define el módulo compartido
`github.com/EduGoGroup/wapp-shared/intents` (tipos + validación), que se consume
como dependencia normal (`go.mod`, `intents v0.1.0`) — sin `replace`.

## API pública

```go
// Construcción. Sin opciones se usan los valores por defecto de abajo.
func New(client llmClient, model string, cfg *intents.Config, opts ...Option) *Classifier
func WithMaxRunes(n int) Option              // techo de entrada; n <= 0 ⇒ DefaultMaxRunes
func WithLLMOptions(o map[string]any) Option // se FUSIONA sobre las opciones por defecto

func (c *Classifier) Classify(ctx context.Context, text string) (Classification, error)
func (c *Classifier) Reload(cfg *intents.Config)
func FastLane(text string) bool

type Classification struct {
    Intent     string
    Params     map[string]string
    Confidence float64
    Metrics    Metrics // costo de la inferencia (total_ms, tokens); también en las degradadas
    Truncado   bool    // el texto superaba el techo y se recortó
}

type Metrics struct {
    TotalMs, LoadMs            int64
    PromptTokens, OutputTokens int
    TokensPerSec               float64
}
```

Constantes exportadas para que el caller no duplique los números a ciegas:

| Constante | Valor | Por qué |
|---|---|---|
| `DefaultMaxRunes` | `4000` | Techo de entrada en **runas** (no bytes). Un pedido real cabe de sobra; 4000 runas de español son ~1000–1300 tokens. |
| `DefaultNumThread` | `5` | Medición de la O0 del Plan 051 sobre el VPS AMD real. No se re-discute sin medir. |
| `DefaultNumCtx` | `4096` | Cabe el prompt de sistema (~600–1500 tok) + el techo de entrada + `num_predict`. Explícito para no depender del default del build de Ollama; y hace de **segundo techo** de costo. Subirlo cuesta RAM permanente (la KV cache crece lineal). |
| `DefaultNumPredict` | `256` | La salida es un JSON corto (30–60 tok medidos); 256 da margen y **acota una generación desbocada**. |

`WithLLMOptions` fusiona: una clave que el caller no menciona se conserva (la
**temperatura 0.1 sobrevive siempre**); una que sí menciona gana como override
deliberado.

## Invariantes (no relajar sin medir)

Cada uno nace de un fallo observado en el prototipo miniWapp:

- **Salida forzada por JSON Schema** con `intent` restringido por `enum`.
- Las **claves de `params` van libres** en el schema (`additionalProperties: string`),
  nunca como `properties`: la gramática de llama.cpp exige orden declarado y perdía
  params silenciosamente. La FORMA la fuerza la gramática; la SEMÁNTICA la valida
  `sanitizeParams`.
- **`sanitizeParams`**: solo params declarados por la intención Y cuyo valor aparezca
  en el mensaje del cliente. Elimina params copiados de los ejemplos del prompt.
- **`think:false`** solo si el modelo tiene la capability `thinking` (qwen3):
  enviarlo a un modelo sin ella es error de Ollama.
- **Temperatura 0.1** (determinismo).
- **Few-shot desde el contrato**: los ejemplos valen más que las instrucciones.
- **FastLane antes del LLM**: números cortos, sí/no/ok, vacío ⇒ no clasificar (0 ms).
- **Umbral de confianza**: por debajo ⇒ `desconocido`. Nunca peor que el flujo clásico.
- **Techo de entrada dentro de `Classify`**, no en el caller: así protege por igual
  al worker cajero (que concatena un lote) y al camino inline; ningún llamador puede
  olvidárselo. Se recorta por **runas**, jamás por bytes. Sin techo, pegar ~65 KB en
  un chat basta para abrir el circuit breaker del Edge y apagar el clasificador de
  **todas** las sesiones: es la denegación de servicio más barata que existía.
- **`sanitizeParams` se aplica contra el texto TRUNCADO**, no contra el original.
  El invariante real es «el valor estaba en lo que el modelo **leyó**». Un valor que
  solo aparece en la cola cortada no pudo extraerse de ahí: el modelo lo alucinó y
  coincidió por casualidad — justo el fallo que el allowlist existe para matar.
- **Opciones del modelo explícitas** (`num_thread`, `num_ctx`, `num_predict`): no se
  depende del default del build de Ollama ni del Modelfile, que cambian entre
  versiones.
- **Las métricas de Ollama se propagan** en `Classification.Metrics` (también en las
  salidas degradadas): son justo los casos que hay que medir. El log del caller lleva
  `total_ms` y tokens, **nunca el texto clasificado** (INV-051.1).

## Batería de validación

`classifier/battery_test.go` corre 12 casos (10 contra Ollama real, 2 de FastLane sin
LLM), incluido el **canario** `"quiero 3 pizzas de pepperoni"` ⇒ `crear_pedido` con
`cantidad=3` (si `cantidad` desaparece, el schema volvió a declarar `properties`).

```bash
# Requiere Ollama en 127.0.0.1:11434 con qwen3:1.7b cargado.
make battery
# Overrides: WAPP_INTENT_TEST_URL, WAPP_INTENT_TEST_MODEL.
```

La batería vive tras el **build tag `ollama`**: sin `-tags ollama` ni siquiera se
compila, y **con** el tag la ausencia de Ollama es un **fallo**, no un salto. Antes
no tenía tag y se saltaba sola: corría en cada CI y se saltaba en cada CI, en
silencio, aparentando cobertura que no existía. Los gates (`make check`,
`make ci-local`) corren **solo lo unitario**; la batería es una corrida deliberada
en una máquina con modelo. El lint sí la analiza (`build-tags: [ollama]` en
`.golangci.yml`) para que no se pudra.

## Comandos

```bash
make build       # compila
make test        # tests unitarios (sin Ollama)
make test-race   # tests con detector de carreras
make check       # fmt + vet + test-race + lint (puerta local)
make battery     # batería contra Ollama real (-tags ollama; requiere el modelo)
```
