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

// llmClient es la dependencia del clasificador hacia el LLM local. La cumple
// *ollama.Client; se declara como interfaz para desacoplar y poder mockear.
type llmClient interface {
	Chat(ctx context.Context, req ollama.ChatRequest) (*ollama.ChatResponse, error)
	SupportsThinking(ctx context.Context, model string) bool
}

// Classification es el contrato de salida del clasificador: la intención elegida,
// los parámetros extraídos (ya saneados) y la confianza del modelo. NO lleva
// texto de respuesta: en wApp la respuesta al cliente la produce el Cloud (Motor
// de Flujos), nunca el LLM del Edge.
type Classification struct {
	Intent     string            `json:"intent"`
	Params     map[string]string `json:"params"`
	Confidence float64           `json:"confidence"`
}

// Classifier clasifica mensajes contra un contrato de intenciones dado.
type Classifier struct {
	ollama llmClient
	model  string

	mu     sync.RWMutex
	cfg    *intents.Config
	prompt string
	schema json.RawMessage
}

// New crea un clasificador para un modelo y un contrato de intenciones. El prompt
// y el schema se derivan del contrato una sola vez aquí (y en cada Reload).
func New(client llmClient, model string, cfg *intents.Config) *Classifier {
	c := &Classifier{ollama: client, model: model}
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
func (c *Classifier) Classify(ctx context.Context, text string) (Classification, error) {
	c.mu.RLock()
	cfg, prompt, schema := c.cfg, c.prompt, c.schema
	c.mu.RUnlock()

	messages := []ollama.Message{
		{Role: "system", Content: prompt},
		{Role: "user", Content: text},
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
		// Temperatura baja: clasificar quiere determinismo, no creatividad.
		Options: map[string]any{"temperature": 0.1},
	})
	if err != nil {
		return Classification{}, fmt.Errorf("classifier: inferencia falló: %w", err)
	}

	out, ok := parseClassification(resp.Content())
	if !ok {
		// Aun con schema forzado conviene un fallback: derivar a "desconocido"
		// deja que el Cloud caiga al flujo clásico en vez de romper el turno.
		return Classification{Intent: intents.ReservedUnknown}, nil
	}

	// La gramática forzó la FORMA; aquí se valida la SEMÁNTICA.
	out.Params = sanitizeParams(out.Params, paramsFor(cfg, out.Intent), text)

	// Por debajo del umbral, mejor "desconocido" que una acción equivocada: el
	// sistema nunca queda peor que el flujo de números.
	if out.Confidence < cfg.UmbralConfianza {
		out.Intent = intents.ReservedUnknown
		out.Params = nil
	}
	return out, nil
}

// parseClassification decodifica la salida del modelo. El bool es false si el
// JSON es ilegible (el caller degrada a "desconocido" sin tratarlo como error).
func parseClassification(content string) (Classification, bool) {
	var out Classification
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		return Classification{}, false
	}
	return out, true
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
