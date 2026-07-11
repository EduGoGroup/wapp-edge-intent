package classifier

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/EduGoGroup/wapp-edge-intent/ollama"
)

// TestBattery es la batería de validación del clasificador contra Ollama REAL.
// Reproduce los 12 casos medidos en el prototipo miniWapp: 2 resueltos por el
// carril rápido (0 ms, sin LLM) y 10 clasificados por el modelo.
//
// Se salta sola si Ollama no responde en 2 s (CI sin modelo). Overrides por env:
// WAPP_INTENT_TEST_URL (default http://127.0.0.1:11434) y WAPP_INTENT_TEST_MODEL
// (default qwen3:1.7b).
func TestBattery(t *testing.T) {
	url := envOr("WAPP_INTENT_TEST_URL", "http://127.0.0.1:11434")
	model := envOr("WAPP_INTENT_TEST_MODEL", "qwen3:1.7b")

	client := ollama.New(url)
	healthCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Health(healthCtx); err != nil {
		t.Skipf("Ollama no disponible en %s (%v); se salta la batería", url, err)
	}

	cfg := loadFixture(t)
	clf := New(client, model, cfg)

	cases := []batteryCase{
		// Carril rápido (0 ms): en wApp los resuelve el fastlane/Motor de Flujos.
		{msg: "2", fast: true},
		{msg: "1", fast: true}, // "1" tras una encuesta: voto, no clasificación
		// LLM.
		{msg: "quiero 3 pizzas de pepperoni", wantIntent: "crear_pedido", mustHave: []string{"producto"}, wantExact: map[string]string{"cantidad": "3"}},
		{msg: "donde esta mi pedido 4512", wantIntent: "estado_pedido", wantExact: map[string]string{"numero_pedido": "4512"}},
		{msg: "a que hora abren el sabado?", wantIntent: "horario_atencion"},
		{msg: "necesito ayuda de un humano", wantIntent: "hablar_humano"},
		{msg: "asdfgh qwerty", wantIntent: "desconocido"},
		{msg: "dame la lista de productos", wantIntent: "listar_productos"},
		{msg: "cuanto cuesta la pizza margarita", wantIntent: "precio_producto", mustHave: []string{"producto"}},
		{msg: "ver mi carrito", wantIntent: "ver_carrito"},
		{msg: "confirma mi pedido", wantIntent: "confirmar_pedido"},
		{msg: "cuanto saldo tengo?", wantIntent: "consultar_saldo"},
	}

	aciertos := 0
	t.Logf("%-38s %-8s %-18s %-8s %s", "MENSAJE", "VIA", "INTENT", "LAT", "PARAMS")
	for _, tc := range cases {
		if runBatteryCase(t, clf, tc) {
			aciertos++
		}
	}
	t.Logf("ACIERTOS: %d/%d", aciertos, len(cases))

	// Canario del bug de la gramática (invariante 2): si "cantidad" desaparece de
	// los params, algo volvió a declarar las claves como properties en el schema.
	// Se corre 3 veces para descartar variabilidad del modelo.
	t.Run("canario_gramatica", func(t *testing.T) {
		const canary = "quiero 3 pizzas de pepperoni"
		for i := 1; i <= 3; i++ {
			start := time.Now()
			ctx, c := context.WithTimeout(context.Background(), 60*time.Second)
			got, err := clf.Classify(ctx, canary)
			c()
			if err != nil {
				t.Fatalf("rep %d: Classify falló: %v", i, err)
			}
			t.Logf("canario rep %d: intent=%s params=%v lat=%s", i, got.Intent, got.Params, time.Since(start).Round(time.Millisecond))
			if got.Intent != "crear_pedido" {
				t.Errorf("rep %d: intent = %q, se esperaba crear_pedido", i, got.Intent)
			}
			if got.Params["cantidad"] != "3" {
				t.Errorf("rep %d: cantidad = %q, se esperaba 3 — el schema puede haber vuelto a declarar properties", i, got.Params["cantidad"])
			}
			if got.Params["producto"] == "" {
				t.Errorf("rep %d: falta el param producto (got %v)", i, got.Params)
			}
		}
	})
}

// batteryCase describe un caso de la batería: o bien se espera carril rápido
// (fast) o bien una clasificación por LLM con intent y params esperados.
type batteryCase struct {
	msg        string
	fast       bool              // esperado por carril rápido (sin LLM)
	wantIntent string            // "" si es fast
	mustHave   []string          // params que DEBEN estar presentes
	wantExact  map[string]string // pares clave=valor que deben coincidir exactamente
}

// runBatteryCase ejecuta un caso y devuelve true si acertó (log incluido).
func runBatteryCase(t *testing.T, clf *Classifier, tc batteryCase) bool {
	t.Helper()
	if tc.fast {
		if !FastLane(tc.msg) {
			t.Errorf("%q: se esperaba carril rápido (FastLane)", tc.msg)
			return false
		}
		t.Logf("%-38q %-8s %-18s %-8s %s", tc.msg, "fast", "(sin LLM)", "0ms", "-")
		return true
	}
	if FastLane(tc.msg) {
		t.Errorf("%q: no debía caer en carril rápido", tc.msg)
		return false
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	got, err := clf.Classify(ctx, tc.msg)
	cancel()
	if err != nil {
		t.Errorf("%q: Classify falló: %v", tc.msg, err)
		return false
	}
	t.Logf("%-38q %-8s %-18s %-8s %v", tc.msg, "llm", got.Intent, time.Since(start).Round(time.Millisecond), got.Params)

	ok := got.Intent == tc.wantIntent
	if !ok {
		t.Errorf("%q: intent = %q, se esperaba %q", tc.msg, got.Intent, tc.wantIntent)
	}
	for _, k := range tc.mustHave {
		if got.Params[k] == "" {
			t.Errorf("%q: falta el param %q (got %v)", tc.msg, k, got.Params)
			ok = false
		}
	}
	for k, v := range tc.wantExact {
		if got.Params[k] != v {
			t.Errorf("%q: param %q = %q, se esperaba %q", tc.msg, k, got.Params[k], v)
			ok = false
		}
	}
	return ok
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
