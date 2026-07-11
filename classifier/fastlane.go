package classifier

import (
	"regexp"
	"strings"
)

// shortNumberRe reconoce un número corto (1–3 dígitos) como texto completo: una
// opción de menú, un voto de encuesta o un código breve.
var shortNumberRe = regexp.MustCompile(`^\d{1,3}$`)

// fastLaneWords son respuestas de una palabra cuya intención resuelve el Motor de
// Flujos por contexto (confirmaciones, negaciones, acuses), no el clasificador.
var fastLaneWords = map[string]struct{}{
	"si": {}, "sí": {}, "no": {}, "ok": {}, "oka": {}, "okey": {}, "okay": {},
	"vale": {}, "dale": {}, "listo": {}, "sip": {}, "nop": {},
}

// FastLane reporta si un mensaje debe saltarse el LLM (carril rápido, 0 ms).
//
// INVARIANTE (medido en miniWapp): descartar lo trivial ANTES del LLM evita
// malinterpretar un "2" (opción de menú) como texto a clasificar y ahorra la
// inferencia entera. Devuelve true para:
//
//   - texto vacío o solo espacios,
//   - un número corto (1–3 dígitos): opción de menú / voto de encuesta,
//   - sí/no/ok y variantes: confirmaciones que el Motor de Flujos resuelve por
//     contexto de la conversación viva.
//
// Cuando FastLane devuelve true, el caller entrega el mensaje SIN intención: en
// wApp el Cloud (Motor de Flujos) sabe si hay una conversación viva que capture
// esa respuesta corta; el clasificador del Edge no debe interceptarla.
func FastLane(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return true
	}
	if shortNumberRe.MatchString(t) {
		return true
	}
	_, ok := fastLaneWords[strings.ToLower(t)]
	return ok
}
