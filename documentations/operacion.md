# Operación de `wapp-edge-intent`

Cómo se compila, se prueba, se publica y se depura. Es una **librería sin binario**: aquí no se
«arranca» nada — se compila, se testea, y se **consume** desde `wapp-edge-agent`.

---

## 🔴 Antes de nada: dos avisos que aplican a todo el ecosistema wApp

### 1. Un PR **no valida nada**. El gate real es local.

`.github/workflows/ci.yml:11-12` es `on: workflow_dispatch`: **no se dispara con push ni con
pull request**. El propio fichero lo explica (`ci.yml:4-7`): *«Régimen local (2026-08-01): la
validación continua vive en la Mac de desarrollo»*. Un PR verde en GitHub significa exactamente
«nadie lo ha comprobado».

El único workflow automático del repo es `sync-main-to-dev.yml`
(`on: push: branches: [main]`), y **no valida nada**: solo deja `dev` alineada con `main` después
de publicar. Existe porque el 2026-08-08 `dev` quedó por detrás de `main` en varios repos y hubo
que repararlo a mano (`sync-main-to-dev.yml:5-12`).

⇒ **Antes de mergear, corre `make ci-local` y mira el resultado con tus ojos.**

### 2. Un `rc=0` cuenta igual un `--- SKIP` que un `--- PASS`. **Cuenta los SKIP.**

`go test` devuelve 0 tanto si el test pasó como si se saltó solo. En el ecosistema los tests de
integración se saltan **sin hacer ruido** cuando falta la variable de base de datos, y un «todo
verde» acaba siendo «no corrió nada».

En **este** repo el problema tiene una defensa arquitectónica y hay que preservarla: la batería
contra Ollama real vive tras `//go:build ollama` (`classifier/battery_test.go:1`), así que **sin
el tag ni se compila** — no puede saltarse en silencio porque no existe. Y **con** el tag, la
ausencia de Ollama es `t.Fatalf`, no `t.Skipf` (`classifier/battery_test.go:33`). El motivo está
escrito (`classifier/battery_test.go:17-21`): antes no tenía tag, *«corría en cada CI y se saltaba
en cada CI, en silencio, dando la falsa sensación de estar cubriendo algo»*.

**Cómo contar de verdad:**

```bash
go test -v ./... -count=1 2>&1 | grep -c '^--- SKIP'   # debe dar 0
go test -v ./... -count=1 2>&1 | grep -c '^--- FAIL'   # debe dar 0
go test -v ./... -count=1 2>&1 | grep -c '^=== RUN'    # cuántos casos corrieron de verdad
```

**Estado medido el 2026-08-30 sobre HEAD `8ab68d4`:** `=== RUN` = **42** · `--- PASS` (nivel
superior) = **30** · `--- SKIP` = **0** · `--- FAIL` = **0**.
Y **lee el código de salida sin pipe**: un `| grep` se traga el `rc` del `go test`.

---

## 1. Arranque local (o más bien: preparación local)

Requisitos: **Go 1.26.5** exacto (`go.mod:3`, `Makefile:4`, `.github/workflows/ci.yml:15`) y, solo
para la batería, **Ollama vivo** con `qwen3:1.7b` cargado.

```bash
git clone https://github.com/EduGoGroup/wapp-edge-intent.git
cd wapp-edge-intent
go mod download
make build        # go build ./... — compila las librerías; NO deja artefacto
```

`make build` **no produce ejecutable**: no hay `cmd/` ni `func main`. Que `.gitignore:2` reserve
`/wapp-edge-intent` bajo «Binarios compilados» es un residuo, no una pista.

### Trabajar contra un `wapp-shared` sin publicar

Se usa un `go.work` **local**, que está en `.gitignore:17-19` a propósito y **no se commitea**:

```bash
go work init . ../../shared/wapp-shared/intents   # ajusta la ruta a tu árbol
```

🔴 **Un puerto contra `wapp-shared` se verifica contra el TAG PUBLICADO, no contra el árbol de al
lado.** Con `go.work` puesto compila aquí y rompe en el consumidor. Antes de dar por bueno un
cambio que toque la dependencia: quita el `go.work`, corre `make ci-local`, y solo entonces.

---

## 2. Cómo se prueba: los `make` reales y qué valida cada uno

Todos de `Makefile:1-53`.

| Target | Qué corre exactamente | Qué valida |
|---|---|---|
| `make build` | `go build ./...` | Que compila. Nada más. |
| `make test` | `go test ./... -count=1` | Los 30 unitarios. **No** compila la batería (le falta el tag). |
| `make test-race` | `go test -race ./... -count=1` | Lo mismo + detector de carreras sobre los dos mutex del repo. |
| `make vet` | `go vet ./...` | Estática de la stdlib. |
| `make fmt` | `gofmt -l .`, **falla si imprime algo** | Que no queda fichero sin formatear. |
| `make lint` | `golangci-lint run --timeout=5m` | **16** linters (`.golangci.yml:13-45`, `linters.enable`; los 2 `formatters.enable` no cuentan como linters en la v2 del fichero), incluidos `gosec`, `gocyclo` (umbral 15), `errcheck` con `check-blank`, `errorlint`, `nilerr`, `nilnil`, `contextcheck`. |
| **`make check`** | `fmt vet test-race lint` | **La puerta local antes de pushear.** |
| **`make ci-local`** | `fmt vet lint test-race build` | **El gate real** — réplica exacta del `ci.yml` que nadie dispara. |
| **`make ci-docker`** | `make ci-local` dentro de `golang:1.26.5-bookworm` + golangci-lint `v2.12.2` | Que no dependes de tu toolchain local. **Requiere Docker.** |
| **`make battery`** | `go test -tags ollama ./classifier -run TestBattery -count=1 -v` | **12 casos contra Ollama REAL** (2 de FastLane sin LLM + 10 por LLM), más el subtest `canario_gramatica`. |

⚠️ **`ci-local` y `ci-docker` no dicen lo mismo.** Es un hallazgo conocido del ecosistema: hay
commits con `ci-local` verde y `ci-docker` rojo. Si vas a publicar, corre los dos.

⚠️ El lint **sí** analiza la batería aunque los gates no la ejecuten: `.golangci.yml:9-11` declara
`build-tags: [ollama]` *«sin esto el fichero quedaría fuera de toda verificación estática y se
pudriría en silencio»*. **No quites ese bloque.**

### La batería (`make battery`) — qué necesita y qué mide

Necesita **Ollama vivo en `127.0.0.1:11434`** con el modelo cargado. Overrides:
`WAPP_INTENT_TEST_URL` y `WAPP_INTENT_TEST_MODEL` (defaults `http://127.0.0.1:11434` y
`qwen3:1.7b`, `classifier/battery_test.go:26-27`).

Sondea salud con un plazo de **2 s** (`classifier/battery_test.go:29-33`) y, si Ollama no está,
**falla**. Los 12 casos están en `classifier/battery_test.go:39-55`. El subtest
`canario_gramatica` (`:68-89`) repite **tres veces** `"quiero 3 pizzas de pepperoni"` y exige
`cantidad == "3"`: es el centinela de que el schema no volvió a declarar `properties` en `params`.

🔴 **Recuerda qué acredita y qué no**: la batería prueba `classifier/`, que **hoy no lo llama
nadie**. Verde aquí ≠ producción correcta. Lo que corre en campo es `ollama/`.

---

## 3. Cómo se publica una versión

Este repo **no tiene `release.yml`**: solo hay `ci.yml` y `sync-main-to-dev.yml` en
`.github/workflows/`. **El tag se corta a mano.**

Flujo del ecosistema (`feature → dev → main → tag`), con la excepción declarada de que
`wapp-shared` necesita `main` para cortar tags:

1. Trabaja en rama de feature. Mergea a **`dev`**.
2. `make ci-local` **y** `make ci-docker` verdes en `dev`. Cuenta los SKIP (§0).
3. Actualiza `CHANGELOG.md`: sube el bloque `[Unreleased]` a `## [X.Y.Z] — AAAA-MM-DD`.
   ⚠️ En un módulo del monorepo `wapp-shared` la cabecera va **sin la «v»** (`## [0.1.0]`); aquí,
   que es repo propio, se sigue el mismo formato — mira los bloques `## [0.3.0]` existentes.
4. `dev` → **`main`**.
5. Corta el tag **sobre `main`**: `git tag vX.Y.Z && git push origin vX.Y.Z`.
   Tags publicados hoy: `v0.1.0` (`f3ced8f`, 2026-07-10), `v0.2.0` (`41dd655`, 2026-08-16),
   `v0.3.0` (`8ab68d4`, 2026-08-24 = **HEAD**).
6. `sync-main-to-dev.yml` deja `dev` alineada solo.
7. **Realinea el consumidor**: `wapp-edge-agent/go.mod:8` apunta hoy a
   `github.com/EduGoGroup/wapp-edge-intent v0.3.0`. Sin este paso el release no llega a campo.

### SemVer aquí, en concreto

`v0.3.0` fue **menor y estrictamente aditiva**: `ChatRequest` ganó un campo opcional, `ChatResponse`
y `Metrics` uno cada uno, más dos constantes y una función. Se verificó **compilando el uso real
del consumidor** contra la copia nueva (`CHANGELOG.md:11-20`). Es el estándar a mantener: un cambio
en la API pública se valida contra el llamante, no contra la intuición.

### Cuando GitHub Actions no está disponible

El régimen ya es local por decisión (2026-08-01), así que no hay «plan B» que activar: `ci-local`
y `ci-docker` **son** el gate. Este repo es **público**, y en repos públicos los minutos de Actions
no consumen la cuota de la cuenta (`sync-main-to-dev.yml:14-16`) — por eso el sync sí puede
permitirse correr solo.

---

## 4. Depuración cuando falla

### El síntoma llega desde el Edge, no desde aquí

Esta pieza no tiene log propio ni proceso propio. **Lo que se registra en ejecución lo registra el
consumidor**: el Edge Agent loguea las métricas de cada inferencia
(`wapp-edge-agent/internal/app/cajero/inferencia.go:150` consume `ollama.Metrics.PromptMs` y
`ChatResponse.EvalDuration`) y clasifica el «calor del prefijo» en tres regímenes
(`frío` / `templado` / `caliente`) con los dos bordes configurables. En el VPS de UAT los logs
**no pasan por journald**: cada unidad escribe con `StandardOutput=append:` a un fichero propio
bajo `/root/source/wApp/logs/`.

### Comprobaciones, de la más barata a la más cara

```bash
# 1. ¿Ollama responde? — la misma ruta que usa Client.Health
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:11434/api/tags     # 200

# 2. ¿Qué modelo está cargado y hasta cuándo?
ollama ps        # UNTIL "Forever" ⇒ keep_alive infinito en vigor

# 3. ¿El modelo tiene la capability "thinking"? — lo que consulta SupportsThinking
curl -s http://127.0.0.1:11434/api/show -d '{"model":"qwen3:1.7b"}' | head -c 400
```

⚠️ **`ollama ps` puede decir «Forever» sin que haya nada útil vivo.** Es un hallazgo medido del
ecosistema: la entrada puede seguir listada mientras la **caché de prefijos** ya se perdió. Son
**dos cachés distintas** — el modelo cargado y el prefijo caliente — y precalentar con un prompt
trivial calienta la primera pero **no** la segunda.

### Los tres fallos que más cuesta ver

| Síntoma | Causa probable | Dónde mirar |
|---|---|---|
| Toda inferencia tarda **×3** desde el arranque y no se recupera hasta reiniciar | Un fallo de red al arrancar cacheó `caps[modelo] = nil` **sin TTL**, y desde entonces no se manda `think:false` | `ollama/client.go:236-238`. Ver `deuda.md`, D2. **Se cura reiniciando el proceso.** |
| `ListModels` devuelve **lista vacía y `error == nil`** | Ollama respondió `500` con cuerpo `{}` y nadie mira el status | `ollama/client.go:281-299`. Ver `deuda.md`, D3. |
| Un `keep_alive` que «se puso» y el runner se muere igual a los 5 min | Se metió dentro de `options`: Ollama **ignora en silencio** las claves desconocidas de `options` | Tiene que ir en primer nivel — `ollama/client.go:99-102`, `ollama/client.go:119` |

### Reproducir sin Ollama

Los 10 tests de `ollama/client_test.go` levantan un `httptest.Server` y afirman sobre el **cuerpo
exacto que viaja por el cable** (`TestChatKeepAliveEnElWire`, `ollama/client_test.go:220`). Si
dudas de qué manda el cliente, ese es el sitio: es la forma que viaja, no la cómoda.

---

## 5. Qué hay de esta pieza en UAT

- **No hay checkout de este repo en el VPS.** Entra como **módulo Go** compilado dentro de
  `wapp-agent` y `wapp-ctl`, en la versión **`v0.3.0`** — el último tag, y `main` no tiene commits
  posteriores a él.
- Ollama en UAT: versión **0.32.6**, sin GPU (100 % CPU), un único modelo cargado (`qwen3:1.7b`,
  1,9 GB, `context 4096`, `Q4_K_M`), `CPUAffinity=0-4` (5 vCPU), `MemoryMax=8 GiB` con 3,53 GB en
  uso (41 %), y `OLLAMA_KEEP_ALIVE=-1` / `OLLAMA_NUM_PARALLEL=1` / `OLLAMA_MAX_LOADED_MODELS=1` en
  un drop-in de systemd.
- 🔴 **Ningún binario de wApp sabe decir su versión**: no puedes preguntarle al proceso qué versión
  de este módulo lleva dentro. Se deduce del `go.mod` del checkout que lo compiló.
