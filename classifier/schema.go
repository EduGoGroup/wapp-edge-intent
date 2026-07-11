package classifier

import (
	"encoding/json"

	"github.com/EduGoGroup/wapp-shared/intents"
)

// buildSchema construye el JSON Schema que Ollama usará como `format` para forzar
// la forma de la salida del modelo. Con modelos pequeños esto es la diferencia
// entre "a veces JSON" y "siempre JSON".
//
// INVARIANTE (bug medido en miniWapp): las claves de `params` van LIBRES —solo
// `additionalProperties: {"type":"string"}`—, NUNCA declaradas como `properties`.
// La gramática de llama.cpp exige las propiedades EN EL ORDEN declarado; si el
// modelo emite primero una clave posterior, las anteriores quedan prohibidas.
// Así qwen3 acababa metiendo la cantidad de un pedido bajo otra clave (la única
// que la gramática aún le permitía) y el "3" se perdía en silencio. No alucinó:
// lo acorraló la gramática. División correcta: la gramática fuerza la FORMA, el
// código (sanitizeParams) valida la SEMÁNTICA.
func buildSchema(cfg *intents.Config) json.RawMessage {
	// El enum incluye los intents del contrato + el reservado "desconocido", que
	// el modelo debe poder emitir aunque no esté declarado como intent.
	enum := make([]string, 0, len(cfg.Intents)+1)
	for _, in := range cfg.Intents {
		enum = append(enum, in.Name)
	}
	enum = append(enum, intents.ReservedUnknown)

	s := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"intent":     map[string]any{"type": "string", "enum": enum},
			"confidence": map[string]any{"type": "number"},
			"params": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]string{"type": "string"},
			},
		},
		"required": []string{"intent", "confidence", "params"},
	}
	raw, err := json.Marshal(s)
	if err != nil {
		// La entrada es estática: no puede fallar. El fallback mínimo evita
		// devolver un schema vacío si algún día cambia la construcción.
		return json.RawMessage(`{"type":"object"}`)
	}
	return raw
}
