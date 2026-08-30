# `wapp-edge-intent` — portal de la pieza

Librería Go de **dos paquetes y ningún binario** que vive en el Edge del ecosistema wApp
(el núcleo que corre 24/7 en el equipo del cliente). `ollama/` es un cliente HTTP mínimo de
la API REST local de Ollama; `classifier/` convierte un mensaje de WhatsApp en
`{intent, params, confidence}` con un LLM pequeño local. Se compila **dentro** del binario del
Edge Agent, que es su único consumidor del ecosistema.

## 🔴 Léelo antes que nada: el repo se llama «clasificador» y su clasificador no lo llama nadie

Desde el **2026-08-24** el paquete `classifier/` tiene **cero llamantes vivos**. El único
consumidor del ecosistema (`wapp-edge-agent`) lo importa en **un solo fichero**
(`internal/app/cajero/cajero.go:46`) y **solo para reexportar cuatro enteros**
(`DefaultMaxRunes`, `DefaultNumThread`, `DefaultNumPredict`, `DefaultNumCtx`, en
`internal/app/cajero/cajero.go:75-81`). No hay una sola llamada a `Classify(` ni a `FastLane(`
fuera de comentarios.

La causa está escrita en el propio consumidor, `wapp-edge-agent/cmd/agent/cajero.go:112-116`:
al pasar de **push** a **pull** (ADR-0045, ejecutado el 2026-08-24) el Edge dejó de clasificar por
iniciativa propia, y el `classifier.New(...)` que había ahí se retiró entero. Hoy el Cloud arma el
prompt y el Edge solo **sirve** la inferencia.

Son **609 líneas de código y 674 de test** que se siguen compilando, testeando y linteando **sin
consumidor vivo**. Es una **decisión pendiente sin dueño**: o vuelve a usarse, o se retira. No hay
plan ni micro-plan que la reclame. Escríbelo así, sin suavizar.

Lo que **sí** está vivo es `ollama/`: se importa en 4 ficheros no-test del agente, y su constante
`DefaultKeepAliveSeconds` (`ollama/client.go:63`) **es el default del `keep_alive` del Edge** —
`wapp-edge-agent/internal/infra/config/config.go:280` no escribe el número, lo importa de aquí.

## Para qué existe

Materializa el **ADR-0020** (LLM local en el Edge como pre-clasificador), construido por el
**Plan 029** del repo de documentación del ecosistema. Vive en repo **propio y público** porque
no toca credenciales, ni la DEK, ni el Lease: no hay nada zero-knowledge que proteger aquí
(`.github/workflows/ci.yml:10` — *«Módulos wApp públicos: sin GOPRIVATE ni token»*).

⚠️ El ADR-0020 está **CADUCO** en su núcleo (enmienda E-3, ejecutada por el ADR-0045). Lo que
sobrevive del ADR cambió de sede: el prompt, el schema y el saneo de la señal los construye hoy el
Cloud. Este repo aporta el cliente de Ollama y cuatro constantes calibradas.

## Índice

| Documento | Qué contiene |
|---|---|
| [`constitucion.md`](constitucion.md) | 🔴 **El más importante.** Invariantes que no se pueden violar (los del ecosistema que aplican + los propios), tecnología y versiones reales, convenciones y **trampas conocidas**. |
| [`arquitectura.md`](arquitectura.md) | Los dos paquetes por dentro, el mapa de ficheros, por qué no hay binario y dónde vive el punto de entrada real. Con diagrama. |
| [`contratos.md`](contratos.md) | Lo que otros consumen: API pública de los dos paquetes, las 4 rutas de Ollama que **consume**, variables de entorno, ficheros y esquemas (spoiler: cero). |
| [`operacion.md`](operacion.md) | Cómo se compila, se prueba (los `make` reales), se publica una versión y se depura. Incluye el aviso de que **un PR no valida nada**. |
| [`deuda.md`](deuda.md) | La deuda viva con `fichero:línea`: el corte por bytes de `sanitizeParams`, la caché de capabilities sin TTL, el código muerto y las afirmaciones falsas del `README.md` del repo. |

## Estado de un vistazo

- Rama `main`, HEAD `8ab68d4` (2026-08-24). Tags publicados: `v0.1.0`, `v0.2.0`, `v0.3.0`.
- `main` ≡ `dev` ≡ `origin/main`. Árbol limpio. 17 commits en total.
- Gates locales verdes: `go build`, `go vet`, `go test -race` — 30 tests, 42 subtests, **0 SKIP**.
- Desplegado en UAT **como módulo Go dentro de `wapp-agent` y `wapp-ctl`** (`v0.3.0`); **no hay
  checkout de este repo en el VPS**.
