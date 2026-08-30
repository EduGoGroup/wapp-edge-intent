# CLAUDE.md — `wapp-edge-intent`

Portal corto. **La verdad vive en [`documentations/`](documentations/README.md)**; esto solo apunta.

## Qué es

Librería Go de **dos paquetes y ningún binario**, compilada dentro del Edge Agent de wApp (el
núcleo que corre 24/7 en el equipo del cliente). `ollama/` es un cliente HTTP mínimo de la API REST
local de Ollama; `classifier/` convierte un mensaje de WhatsApp en `{intent, params, confidence}`.
Repo **público**: no toca credenciales, ni la DEK, ni el Lease.

## 🔴 Lo primero que tienes que saber

**El repo se llama «clasificador de intenciones» y su paquete `classifier/` no lo llama nadie**
desde el 2026-08-24. El único consumidor (`wapp-edge-agent`) lo importa en un fichero
(`internal/app/cajero/cajero.go:46`) **solo para reexportar cuatro enteros**
(`internal/app/cajero/cajero.go:75-81`). Cero llamadas a `Classify(` o `FastLane(`. La causa está
escrita en `wapp-edge-agent/cmd/agent/cajero.go:112-116`: al pasar de push a pull (ADR-0045) el
`classifier.New` se retiró entero. Son **609 líneas + 674 de test** que se mantienen y lintean sin
consumidor, y es una **decisión pendiente sin dueño**: o vuelve a usarse, o se retira.

**Lo vivo es `ollama/`.** Un test verde de `classifier/` no acredita nada de producción.

## Las cinco reglas innegociables

1. **Se corta por RUNAS, jamás por bytes.** `truncateRunes` (`classifier/classifier.go:340-355`) lo
   hace bien; `sanitizeParams` (`classifier/sanitize.go:31`, `vl[:4]`) lo hace **mal** y el fallo
   está reproducido. No copies ese patrón.
2. **`ollama/client.go:63` (`DefaultKeepAliveSeconds`) NO es un detalle interno**: es el default del
   `keep_alive` del Edge entero — `wapp-edge-agent/internal/infra/config/config.go:280` lo importa
   en vez de escribir el número. Y `keep_alive` va en **primer nivel** de `/api/chat`: dentro de
   `options`, Ollama lo ignora **en silencio**.
3. **Nada de secretos, criptografía, gRPC ni broker aquí.** Zero-knowledge: la nube nunca accede a
   credenciales ni llaves privadas (protege **llaves**, no el contenido de negocio, que sí sube a
   propósito). La **DEK** la custodia el cliente y jamás cruza un contrato; el **Lease** lo emite y
   revoca el servidor, y es el kill-switch anti-clon: ninguno aparece aquí, y no debe. La
   concurrencia se resuelve con Go — **sin Redis ni broker en el Edge**.
4. **Copia-adaptación, nunca dependencia.** Prohibido importar cualquier repo `edugo-*` (el
   namespace `EduGoGroup/wapp-*` sí es legítimo). El código compartido interno vive en
   **`wapp-shared`**, monorepo multi-módulo con tags `<modulo>/vX.Y.Z`; aquí se consume
   `wapp-shared/intents v0.1.0`, **sin `replace`**. Un puerto se verifica contra el **tag
   publicado**, no contra el árbol de al lado.
5. **Un PR no valida nada.** `.github/workflows/ci.yml` es `workflow_dispatch`. El gate real es
   local: `make ci-local` **y** `make ci-docker`. Y **cuenta los `--- SKIP`**: un `rc=0` los cuenta
   igual que un `--- PASS`.

## Índice de `documentations/`

| Documento | Qué contiene |
|---|---|
| [`README.md`](documentations/README.md) | Portal de la pieza: qué es, para qué existe, estado de un vistazo. |
| [`constitucion.md`](documentations/constitucion.md) | 🔴 **El más importante.** Invariantes (los del ecosistema + los 12 propios) con cómo se comprueban y qué test los vigila, tecnología real y **13 trampas conocidas**. |
| [`arquitectura.md`](documentations/arquitectura.md) | Los dos paquetes, el flujo de `Classify`, por qué no hay binario y dónde vive el punto de entrada real. Con diagrama. |
| [`contratos.md`](documentations/contratos.md) | API pública de los dos paquetes, las 4 rutas de Ollama que consume, variables de entorno con nombre efectivo, ficheros y esquemas (cero). |
| [`operacion.md`](documentations/operacion.md) | Los `make` reales y qué valida cada uno, cómo se corta un tag (no hay `release.yml`), y cómo se depura. |
| [`deuda.md`](documentations/deuda.md) | Deuda viva con `fichero:línea`, el código muerto verificado y las afirmaciones falsas del `README.md` del repo. |

## Toolchain

Go **1.26.5** exacto · golangci-lint **v2.12.2** · única dependencia externa `wapp-shared/intents v0.1.0` · `qwen3:1.7b`.
