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
classifier/   Classifier: New(client, model, cfg) + Reload(cfg) + Classify(ctx, text).
              prompt/schema regenerados desde el contrato de intents; sanitizeParams
              como allowlist real; FastLane para descartar lo trivial sin LLM.
```

El contrato de intenciones lo define el módulo compartido
`github.com/EduGoGroup/wapp-shared/intents` (tipos + validación). Aquí se consume
por réplica local (`replace` en `go.mod`) hasta cortar `intents/v0.1.0`.

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

## Batería de validación

`classifier/battery_test.go` corre 12 casos (10 contra Ollama real, 2 de FastLane sin
LLM), incluido el **canario** `"quiero 3 pizzas de pepperoni"` ⇒ `crear_pedido` con
`cantidad=3` (si `cantidad` desaparece, el schema volvió a declarar `properties`).

```bash
# Requiere Ollama en 127.0.0.1:11434 con qwen3:1.7b cargado.
make battery
# Overrides: WAPP_INTENT_TEST_URL, WAPP_INTENT_TEST_MODEL.
```

Sin Ollama (p. ej. en CI), la batería se **salta sola** (`/api/tags` no responde en 2 s).

## Comandos

```bash
make build       # compila
make test        # tests unitarios (+ batería si hay modelo)
make test-race   # tests con detector de carreras
make check       # fmt + vet + test-race + lint (puerta local)
```
