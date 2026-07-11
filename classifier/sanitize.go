package classifier

import "strings"

// sanitizeParams es el allowlist real de parámetros (la contraparte semántica de
// la gramática, que solo fuerza la forma). Devuelve solo los params que:
//
//  1. están declarados por la intención elegida (allowed), y
//  2. cuyo valor aparece en el mensaje del cliente.
//
// INVARIANTE (medido en miniWapp): los modelos pequeños a veces "copian" valores
// de los ejemplos del prompt (alucinaron un pedido "887" que el cliente nunca
// escribió). Exigir que el valor aparezca en el mensaje elimina esa clase entera
// de fallo.
//
// La comparación tolera plural/tipeo: si el valor tiene 4+ letras, basta con que
// su prefijo de 4 caracteres aparezca en el mensaje (p. ej. "pizzas" ~ "pizza").
// Devuelve nil si no sobrevive ningún param (nil, no mapa vacío: es la ausencia).
func sanitizeParams(params map[string]string, allowed []string, userMessage string) map[string]string {
	if len(params) == 0 {
		return nil
	}
	msg := strings.ToLower(userMessage)
	clean := make(map[string]string, len(allowed))
	for _, key := range allowed {
		v := params[key]
		if v == "" {
			continue
		}
		vl := strings.ToLower(v)
		if strings.Contains(msg, vl) || (len(vl) >= 4 && strings.Contains(msg, vl[:4])) {
			clean[key] = v
		}
	}
	if len(clean) == 0 {
		return nil
	}
	return clean
}
