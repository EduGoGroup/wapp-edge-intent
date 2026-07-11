package classifier

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/EduGoGroup/wapp-shared/intents"
)

// buildPrompt regenera el prompt de sistema desde el contrato de intenciones.
// Se regenera entero en cada Reload: no hay estado oculto entre configs.
//
// INVARIANTE (medido en miniWapp): los ejemplos (few-shot) valen más que las
// instrucciones. Sin ellos, un modelo de 1–2B falla el mapeo de intenciones y la
// extracción de params. Agregar una intención = agregar 1–2 ejemplos al contrato;
// el prompt y el schema se regeneran solos.
func buildPrompt(cfg *intents.Config) string {
	var b strings.Builder
	b.WriteString(`Eres el enrutador de mensajes de un negocio que atiende clientes por WhatsApp.
Tu única tarea: leer el mensaje del cliente y clasificarlo en una intención para decidir qué endpoint llamar.

Intenciones disponibles:
`)
	for _, in := range cfg.Intents {
		fmt.Fprintf(&b, "- %s: %s", in.Name, in.Descripcion)
		if len(in.Params) > 0 {
			fmt.Fprintf(&b, " (extrae parámetros: %s)", strings.Join(in.Params, ", "))
		}
		b.WriteString("\n")
	}

	// El vocabulario del tenant son pistas de dominio (nombres de productos,
	// jerga) que ayudan al modelo a anclar la extracción; es opcional.
	if len(cfg.Vocabulario) > 0 {
		fmt.Fprintf(&b, "\nVocabulario del negocio (pistas): %s\n", strings.Join(cfg.Vocabulario, ", "))
	}

	fmt.Fprintf(&b, `
Reglas:
- Infiere la intención tanto de texto libre como de palabras clave.
- "confidence" entre 0 y 1 según qué tan seguro estés.
- Si la confianza es baja o el mensaje es ambiguo, usa "%s".
- Extrae en "params" solo lo que el cliente haya dicho explícitamente; no inventes valores.
- Usa en "params" exactamente los nombres de parámetro declarados por la intención elegida; nunca otros nombres.

Ejemplos:
`, intents.ReservedUnknown)

	for _, in := range cfg.Intents {
		for _, ej := range in.Ejemplos {
			// El ejemplo se serializa con la misma forma que debe devolver el
			// modelo: {intent, confidence, params}. Confianza alta fija (0.9):
			// son ejemplos positivos por construcción.
			shot := struct {
				Intent     string            `json:"intent"`
				Confidence float64           `json:"confidence"`
				Params     map[string]string `json:"params"`
			}{Intent: in.Name, Confidence: 0.9, Params: ej.Params}
			raw, err := json.Marshal(shot)
			if err != nil {
				continue
			}
			fmt.Fprintf(&b, "%q -> %s\n", ej.Mensaje, raw)
		}
	}
	b.WriteString("\nResponde únicamente con el JSON.")
	return b.String()
}
