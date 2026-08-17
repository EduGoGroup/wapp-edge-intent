// Package classifier clasifica el mensaje de un cliente de WhatsApp en una
// intención accionable ({intent, params, confidence}) usando un LLM pequeño
// local (Ollama). El contrato de intenciones lo define el módulo compartido
// github.com/EduGoGroup/wapp-shared/intents; aquí solo se consume.
//
// El clasificador es seguro para uso concurrente: Reload intercambia la config
// (prompt + schema) en caliente sin cortar clasificaciones en vuelo.
package classifier

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/EduGoGroup/wapp-edge-intent/ollama"
	"github.com/EduGoGroup/wapp-shared/intents"
)

// Techo de entrada y opciones del modelo (Plan 051 · T2.5).
//
// Son constantes exportadas a propósito: el caller (el worker cajero del Edge)
// las necesita para su config y NO debe duplicar los números a ciegas.
const (
	// DefaultMaxRunes es el techo de entrada por defecto, en RUNAS (no bytes):
	// el texto que se manda al modelo se recorta a este tamaño.
	//
	// POR QUÉ EXISTE: sin techo, un mensaje de ~65 KB pegado en un chat tarda lo
	// bastante como para contar fallos y abrir el circuit breaker compartido del
	// Edge, apagando el clasificador de TODAS las sesiones. Es la vía más barata
	// de denegar el servicio desde fuera, y no cuesta nada cerrarla.
	//
	// POR QUÉ 1000 (y no 4000, que es lo que había): el 4000 se puso a ojo contando
	// tokens de español y NO sobrevivió a la medición contra Ollama real. Falla por
	// DOS motivos independientes, y el segundo es el peor.
	//
	// 1) ENCAJE. El prompt de sistema mide 911 tokens, y la densidad depende del
	// alfabeto (tokens/runa medidos con qwen3:1.7b): español 0,257 · cirílico 0,390 ·
	// CJK 0,666 · emoji 1,000. Con techo 4000, el prompt_eval_count REAL es 1.914
	// (es), 2.472 (ru), 3.577 (CJK) y 4.912 (emoji). Los tres primeros caben en
	// DefaultNumCtx; el emoji NO: medido a num_ctx=4096, Ollama evaluó solo 2.050
	// tokens y DESCARTÓ el 58 % de la entrada.
	//
	// 2) TIEMPO, que ninguna ventana de contexto arregla. A 4000 runas la inferencia
	// tarda 32,6 s en español, 34–71 s en CJK y 119,7 s en emoji. El plazo del worker
	// cajero (15 s) ya se cruza a ~1.500 runas de español y ~500 de CJK: mucho antes
	// de que el contexto se quede corto. Por eso NO se sube num_ctx (ver DefaultNumCtx).
	//
	// La aritmética del 1000: en el peor caso concebible (1,0 tok/runa, emoji puro)
	// son 1.000 tokens; más un prompt de sistema del «contrato rico» que DefaultNumCtx
	// anticipa (~1.500 tok) y la respuesta (DefaultNumPredict, 256) suman 2.756 < 4.096.
	// Cabe SIEMPRE, en cualquier alfabeto y aunque el contrato engorde. Y acota la
	// latencia del peor caso a ~13 s en español y ~26 s en CJK: el español entra en el
	// plazo del worker, y el CJK patológico lo cruza pero lo corta el timeout, que es
	// una degradación SEGURA (el caller entrega el mensaje sin clasificar) — al revés
	// que el desbordamiento de contexto, que devuelve una respuesta segura y falsa.
	//
	// No toca tráfico legítimo: el lote más grande observado en tráfico real (8
	// mensajes concatenados) va de 98 a 196 runas. 1000 es 5× ese máximo.
	DefaultMaxRunes = 1000

	// DefaultNumThread es el número de hilos de inferencia.
	//
	// POR QUÉ 5: lo fijó la medición de la O0 del Plan 051 sobre el VPS AMD real
	// (el ADR-0038 §3 y el design §4 decían «2» y quedaron corregidos). NO se
	// re-discute aquí: si hay que cambiarlo, se cambia midiendo, no opinando.
	DefaultNumThread = 5

	// DefaultNumCtx es la ventana de contexto pedida a Ollama, en tokens.
	//
	// POR QUÉ 4096: tiene que caber el prompt de sistema (que se regenera desde el
	// contrato: intenciones + vocabulario + few-shots; 911 tokens medidos hoy, hasta
	// ~1500 para un contrato rico) MÁS el techo de entrada (1000 tokens en el peor
	// caso, ver DefaultMaxRunes) MÁS la respuesta (DefaultNumPredict). El peor caso
	// suma 2.756, dentro de 4096.
	//
	// Se manda EXPLÍCITO para no depender del default del build de Ollama ni del
	// Modelfile del modelo, que cambian entre versiones. Y hace de segundo techo:
	// aunque el techo de runas fallara, el costo de una inferencia queda acotado.
	//
	// POR QUÉ NO SE SUBE: cuesta RAM permanente en la caja del cliente (la KV cache
	// crece lineal con num_ctx; medido, pasar a 8192 son +493 MB de RSS) y NO compra
	// nada, porque el problema que aparece antes es el TIEMPO, no el contexto: a
	// techo alto la inferencia se pasa del plazo del worker mucho antes de quedarse
	// sin ventana. La respuesta correcta a una entrada gigante es bajar el techo de
	// entrada, no ensanchar la ventana.
	//
	// QUÉ PASA SI SE DESBORDA (medido, y NO es lo que este comentario decía antes):
	// la entrada que desborda a 4096 no es el CJK —ese cabía, 3.577 tokens— sino el
	// EMOJI, a 1,0 tok/runa. Y al desbordar la clasificación NO «degrada a desconocido
	// en silencio»: Ollama descarta la entrada que no cabe (medido: el 58 %) y el
	// modelo devuelve un intent PLAUSIBLE Y EQUIVOCADO con confianza máxima. En la
	// medición fue "horario_atencion" con confidence: 100, que ni siquiera el umbral
	// atrapaba (ver el minimum/maximum de `confidence` en schema.go). Desbordar es un
	// fallo silencioso y ACTIVO, no una degradación segura: por eso el techo de
	// entrada se calibra para que no ocurra nunca, en ningún alfabeto.
	DefaultNumCtx = 4096

	// DefaultNumPredict es el máximo de tokens que el modelo puede generar.
	//
	// POR QUÉ 256: la salida es un JSON corto y de forma fija
	// ({intent, confidence, params}), que medido ronda los 30–60 tokens; 256 deja
	// 4–8× de margen para una intención con varios params largos y, sobre todo,
	// acota una generación desbocada (un modelo pequeño que entra en bucle ante
	// una entrada degenerada no puede quemar tiempo sin límite).
	DefaultNumPredict = 256
)

// llmClient es la dependencia del clasificador hacia el LLM local. La cumple
// *ollama.Client; se declara como interfaz para desacoplar y poder mockear.
type llmClient interface {
	Chat(ctx context.Context, req ollama.ChatRequest) (*ollama.ChatResponse, error)
	SupportsThinking(ctx context.Context, model string) bool
}

// Metrics es el costo de una inferencia, tal y como lo reporta Ollama. El
// clasificador la propaga en cada Classification para que el caller la registre
// (log Info / contador local); antes se calculaba y se tiraba (Plan 051 · T2.6).
//
// Se declara aquí, y no se reexporta ollama.Metrics, para que el consumidor del
// clasificador no tenga que importar el paquete ollama.
type Metrics struct {
	TotalMs      int64   `json:"total_ms"`
	LoadMs       int64   `json:"load_ms"`
	PromptTokens int     `json:"prompt_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TokensPerSec float64 `json:"tokens_per_sec"`
}

// metricsFrom traduce las métricas del cliente de Ollama al tipo público.
func metricsFrom(m ollama.Metrics) Metrics {
	return Metrics{
		TotalMs:      m.TotalMs,
		LoadMs:       m.LoadMs,
		PromptTokens: m.PromptTokens,
		OutputTokens: m.OutputTokens,
		TokensPerSec: m.TokensPerSec,
	}
}

// Classification es el contrato de salida del clasificador: la intención elegida,
// los parámetros extraídos (ya saneados), la confianza del modelo y el costo de la
// inferencia. NO lleva texto de respuesta: en wApp la respuesta al cliente la
// produce el Cloud (Motor de Flujos), nunca el LLM del Edge. Tampoco lleva el
// texto clasificado (INV-051.1): el caller puede loguear esta struct entera.
type Classification struct {
	Intent     string            `json:"intent"`
	Params     map[string]string `json:"params"`
	Confidence float64           `json:"confidence"`

	// Metrics es el costo de la inferencia (total_ms, tokens). Viene poblada
	// también en las salidas degradadas ("desconocido"): el caller quiere medir
	// justo esos casos.
	Metrics Metrics `json:"metrics"`

	// Truncado indica que el texto de entrada superaba el techo de runas y se
	// recortó antes de mandarlo al modelo. Es observable a propósito: el Edge lo
	// loguea, y un Truncado crónico significa que el techo está mal calibrado
	// para ese tenant (o que alguien está probando a tumbar el clasificador).
	Truncado bool `json:"truncado"`
}

// Classifier clasifica mensajes contra un contrato de intenciones dado.
type Classifier struct {
	ollama llmClient
	model  string

	// maxRunes y llmOpts se fijan en New y NO se vuelven a escribir: se leen sin
	// lock desde Classify. Reload solo intercambia cfg/prompt/schema.
	maxRunes int
	llmOpts  map[string]any

	mu     sync.RWMutex
	cfg    *intents.Config
	prompt string
	schema json.RawMessage
}

// Option ajusta un Classifier en su construcción (patrón funcional). Ver
// WithMaxRunes y WithLLMOptions.
type Option func(*Classifier)

// WithMaxRunes fija el techo de entrada en runas. Con n <= 0 se usa
// DefaultMaxRunes (nunca «sin techo»: quedarse sin techo es el bug que T2.5
// vino a cerrar).
func WithMaxRunes(n int) Option {
	return func(c *Classifier) {
		if n <= 0 {
			n = DefaultMaxRunes
		}
		c.maxRunes = n
	}
}

// WithLLMOptions FUSIONA opciones sobre las que el clasificador manda por
// defecto (temperature, num_thread, num_ctx, num_predict); no las reemplaza. Una
// clave repetida gana la del caller — es un override deliberado —, y las que no
// menciona se conservan: en particular la temperatura 0.1 sobrevive siempre a una
// fusión que no la nombre. Se copia el mapa recibido: mutarlo después no afecta al
// clasificador.
func WithLLMOptions(o map[string]any) Option {
	return func(c *Classifier) {
		for k, v := range o {
			c.llmOpts[k] = v
		}
	}
}

// defaultLLMOptions son las opciones que viajan a Ollama en cada inferencia.
// Devuelve un mapa nuevo en cada llamada (no se comparte estado entre
// clasificadores).
func defaultLLMOptions() map[string]any {
	return map[string]any{
		// Temperatura baja: clasificar quiere determinismo, no creatividad.
		"temperature": 0.1,
		"num_thread":  DefaultNumThread,
		"num_ctx":     DefaultNumCtx,
		"num_predict": DefaultNumPredict,
	}
}

// New crea un clasificador para un modelo y un contrato de intenciones. El prompt
// y el schema se derivan del contrato una sola vez aquí (y en cada Reload). Sin
// opciones se usan DefaultMaxRunes y las opciones de modelo por defecto.
func New(client llmClient, model string, cfg *intents.Config, opts ...Option) *Classifier {
	c := &Classifier{
		ollama:   client,
		model:    model,
		maxRunes: DefaultMaxRunes,
		llmOpts:  defaultLLMOptions(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	c.apply(cfg)
	return c
}

// Reload intercambia el contrato de intenciones en caliente (p. ej. cuando el
// Cloud empuja una config nueva por el stream). Regenera prompt y schema.
func (c *Classifier) Reload(cfg *intents.Config) {
	c.apply(cfg)
}

// apply fija config+prompt+schema bajo el lock de escritura.
func (c *Classifier) apply(cfg *intents.Config) {
	prompt := buildPrompt(cfg)
	schema := buildSchema(cfg)
	c.mu.Lock()
	c.cfg, c.prompt, c.schema = cfg, prompt, schema
	c.mu.Unlock()
}

// Classify infiere la intención de un mensaje. El plazo lo gobierna ctx: si
// Ollama tarda más de lo que el caller tolera, ctx lo corta y el caller degrada
// (entrega el mensaje sin clasificar). Un JSON ilegible del modelo no es error:
// se degrada a "desconocido" para no tumbar el flujo.
//
// El techo de entrada se aplica AQUÍ, no en el caller: así protege por igual al
// worker cajero (que concatena un lote y llama una vez) y al camino inline viejo
// del Edge, que sigue vivo hasta la Ola 3. Ningún llamador puede olvidarse de él.
func (c *Classifier) Classify(ctx context.Context, text string) (Classification, error) {
	c.mu.RLock()
	cfg, prompt, schema := c.cfg, c.prompt, c.schema
	c.mu.RUnlock()

	// Techo de entrada: se recorta por RUNAS, nunca por bytes (cortar bytes parte
	// un carácter multi-byte por la mitad y le manda al modelo un UTF-8 inválido).
	prompted, truncado := truncateRunes(text, c.maxRunes)

	messages := []ollama.Message{
		{Role: "system", Content: prompt},
		{Role: "user", Content: prompted},
	}

	// think:false solo si el modelo tiene la capability; en modelos razonadores
	// (qwen3) la cadena de pensamiento triplica la latencia sin mejorar el
	// resultado de clasificar.
	var think *bool
	if c.ollama.SupportsThinking(ctx, c.model) {
		f := false
		think = &f
	}

	resp, err := c.ollama.Chat(ctx, ollama.ChatRequest{
		Model:    c.model,
		Messages: messages,
		Format:   schema,
		Think:    think,
		Options:  c.llmOpts,
	})
	if err != nil {
		return Classification{}, fmt.Errorf("classifier: inferencia falló: %w", err)
	}
	metrics := metricsFrom(resp.Metrics())

	out, ok := parseClassification(resp.Content())
	if !ok {
		// Aun con schema forzado conviene un fallback: derivar a "desconocido"
		// deja que el Cloud caiga al flujo clásico en vez de romper el turno.
		// Aun degradado se devuelven métricas y Truncado: son justo los casos que
		// el caller quiere ver en el log.
		return Classification{Intent: intents.ReservedUnknown, Metrics: metrics, Truncado: truncado}, nil
	}
	out.Metrics, out.Truncado = metrics, truncado

	// La gramática forzó la FORMA; aquí se valida la SEMÁNTICA.
	//
	// DECISIÓN (T2.5): se sanea contra `prompted` (el texto TRUNCADO), no contra el
	// original. sanitizeParams exige que el valor del param aparezca en el mensaje,
	// y el invariante real que protege es «el valor estaba en lo que el modelo
	// LEYÓ». Si un valor aparece solo en la cola que se cortó, el modelo no pudo
	// extraerlo de ahí: lo copió de los ejemplos del prompt o lo alucinó, y coincidió
	// por casualidad — exactamente la clase de fallo que sanitizeParams existe para
	// matar. Sanear contra el texto completo relajaría el allowlist justo en las
	// entradas más sospechosas (las gigantes). No se pierde ningún param legítimo:
	// un param legítimo, por construcción, vive en lo que el modelo vio.
	out.Params = sanitizeParams(out.Params, paramsFor(cfg, out.Intent), prompted)

	// Por debajo del umbral, mejor "desconocido" que una acción equivocada: el
	// sistema nunca queda peor que el flujo de números.
	//
	// Esta comparación solo significa algo porque el schema acota `confidence` a
	// [0,1] (schema.go): sin ese rango, el modelo puede devolver 100 y ninguna
	// confianza cae nunca por debajo del umbral. Ya pasó, medido.
	if out.Confidence < cfg.UmbralConfianza {
		out.Intent = intents.ReservedUnknown
		out.Params = nil
	}
	return out, nil
}

// truncateRunes recorta s a limit runas y reporta si hubo recorte. Con limit <= 0
// devuelve s intacto (defensa: New nunca deja maxRunes en 0).
//
// El corte es por runas, así que jamás parte un carácter multi-byte: el resultado
// siempre es UTF-8 válido y prefijo exacto de s.
func truncateRunes(s string, limit int) (string, bool) {
	if limit <= 0 {
		return s, false
	}
	// len(s) en bytes es cota superior del número de runas: si no pasa el techo en
	// bytes, tampoco en runas, y nos ahorramos la conversión a []rune (que en el
	// caso patológico de 65 KB es la única asignación grande del camino).
	if len(s) <= limit {
		return s, false
	}
	r := []rune(s)
	if len(r) <= limit {
		return s, false
	}
	return string(r[:limit]), true
}

// classificationWire es la forma que se decodifica de la salida del modelo. Es
// deliberadamente un tipo aparte de Classification: los campos que añade el Edge
// (Metrics, Truncado) los pone el código, y ninguna salida del modelo debe poder
// escribirlos.
type classificationWire struct {
	Intent     string            `json:"intent"`
	Params     map[string]string `json:"params"`
	Confidence float64           `json:"confidence"`
}

// parseClassification decodifica la salida del modelo. El bool es false si el
// JSON es ilegible (el caller degrada a "desconocido" sin tratarlo como error).
func parseClassification(content string) (Classification, bool) {
	var wire classificationWire
	if err := json.Unmarshal([]byte(content), &wire); err != nil {
		return Classification{}, false
	}
	return Classification{
		Intent:     wire.Intent,
		Params:     wire.Params,
		Confidence: wire.Confidence,
	}, true
}

// paramsFor devuelve los params declarados por la intención name en el contrato,
// o nil si la intención no existe (p. ej. "desconocido", que no declara params).
func paramsFor(cfg *intents.Config, name string) []string {
	for i := range cfg.Intents {
		if cfg.Intents[i].Name == name {
			return cfg.Intents[i].Params
		}
	}
	return nil
}
