package classifier

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/EduGoGroup/wapp-edge-intent/ollama"
	"github.com/EduGoGroup/wapp-shared/intents"
)

// loadFixture carga y valida el contrato de la batería (compartido por los tests).
func loadFixture(t *testing.T) *intents.Config {
	t.Helper()
	data, err := os.ReadFile("testdata/intents.json")
	if err != nil {
		t.Fatalf("leer fixture: %v", err)
	}
	cfg, err := intents.ParseAndValidate(data)
	if err != nil {
		t.Fatalf("fixture inválido: %v", err)
	}
	return cfg
}

// stubLLM implementa llmClient: devuelve un contenido fijo y registra el request.
type stubLLM struct {
	content  string
	thinking bool
	lastReq  ollama.ChatRequest
}

func (s *stubLLM) Chat(_ context.Context, req ollama.ChatRequest) (*ollama.ChatResponse, error) {
	s.lastReq = req
	return &ollama.ChatResponse{Message: ollama.Message{Content: s.content}}, nil
}

func (s *stubLLM) SupportsThinking(_ context.Context, _ string) bool { return s.thinking }

// --- schema ---

func TestBuildSchemaParamsAreFreeNotProperties(t *testing.T) {
	cfg := loadFixture(t)
	var s struct {
		Properties struct {
			Intent struct {
				Enum []string `json:"enum"`
			} `json:"intent"`
			Params map[string]json.RawMessage `json:"params"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(buildSchema(cfg), &s); err != nil {
		t.Fatalf("schema ilegible: %v", err)
	}

	// INVARIANTE: params NO declara "properties" (eso reintroduce el bug de la
	// gramática); solo additionalProperties.
	if _, bad := s.Properties.Params["properties"]; bad {
		t.Error("params declara 'properties' — reintroduce el bug de params perdidos")
	}
	if _, ok := s.Properties.Params["additionalProperties"]; !ok {
		t.Error("params debe usar additionalProperties")
	}

	// El enum debe incluir cada intent del contrato + el reservado "desconocido".
	if !slices.Contains(s.Properties.Intent.Enum, intents.ReservedUnknown) {
		t.Error("el enum de intent debe incluir 'desconocido'")
	}
	for _, in := range cfg.Intents {
		if !slices.Contains(s.Properties.Intent.Enum, in.Name) {
			t.Errorf("el enum no incluye el intent %q", in.Name)
		}
	}
}

// --- prompt ---

func TestBuildPromptContainsIntentsAndExamples(t *testing.T) {
	cfg := loadFixture(t)
	p := buildPrompt(cfg)
	for _, in := range cfg.Intents {
		if !strings.Contains(p, in.Name) {
			t.Errorf("el prompt no menciona el intent %q", in.Name)
		}
	}
	// Al menos un ejemplo few-shot debe aparecer serializado.
	if !strings.Contains(p, "quiero 2 pizzas margarita") {
		t.Error("el prompt no incluye los ejemplos few-shot del contrato")
	}
	if !strings.Contains(p, intents.ReservedUnknown) {
		t.Error("el prompt debe instruir el uso de 'desconocido'")
	}
}

// --- sanitizeParams ---

func TestSanitizeParams(t *testing.T) {
	cases := []struct {
		name    string
		params  map[string]string
		allowed []string
		msg     string
		want    map[string]string
	}{
		{
			name:    "conserva params declarados presentes en el mensaje",
			params:  map[string]string{"producto": "pizza", "cantidad": "3"},
			allowed: []string{"producto", "cantidad"},
			msg:     "quiero 3 pizzas de pepperoni",
			want:    map[string]string{"producto": "pizza", "cantidad": "3"},
		},
		{
			name:    "descarta param no declarado por la intención",
			params:  map[string]string{"producto": "pizza", "color": "rojo"},
			allowed: []string{"producto"},
			msg:     "quiero pizza roja",
			want:    map[string]string{"producto": "pizza"},
		},
		{
			name:    "descarta valor copiado del ejemplo (ausente del mensaje)",
			params:  map[string]string{"numero_pedido": "887"},
			allowed: []string{"numero_pedido"},
			msg:     "donde esta mi pedido",
			want:    nil,
		},
		{
			name:    "tolera plural por prefijo de 4 letras",
			params:  map[string]string{"producto": "pizzas"},
			allowed: []string{"producto"},
			msg:     "quiero una pizza",
			want:    map[string]string{"producto": "pizzas"},
		},
		{
			name:    "params vacíos devuelve nil",
			params:  nil,
			allowed: []string{"producto"},
			msg:     "hola",
			want:    nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeParams(tc.params, tc.allowed, tc.msg)
			if !equalMap(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// --- FastLane ---

func TestFastLane(t *testing.T) {
	trueCases := []string{"2", "1", "12", "999", "", "   ", "si", "Sí", "no", "ok", "OK", "dale"}
	for _, c := range trueCases {
		if !FastLane(c) {
			t.Errorf("FastLane(%q) = false, se esperaba true", c)
		}
	}
	falseCases := []string{"quiero 3 pizzas", "1234", "hola que tal", "sipongo"}
	for _, c := range falseCases {
		if FastLane(c) {
			t.Errorf("FastLane(%q) = true, se esperaba false", c)
		}
	}
}

// --- Classify (con stub, sin Ollama) ---

func TestClassifyAppliesThresholdAndSanitize(t *testing.T) {
	cfg := loadFixture(t)

	// Confianza alta: conserva intent y param presente en el mensaje.
	high := &stubLLM{content: `{"intent":"crear_pedido","confidence":0.95,"params":{"producto":"pizza","cantidad":"3"}}`}
	c := New(high, "m", cfg)
	got, err := c.Classify(context.Background(), "quiero 3 pizzas")
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Intent != "crear_pedido" || got.Params["cantidad"] != "3" || got.Params["producto"] != "pizza" {
		t.Errorf("clasificación alta inesperada: %+v", got)
	}

	// Confianza bajo umbral: colapsa a "desconocido" y limpia params.
	low := &stubLLM{content: `{"intent":"crear_pedido","confidence":0.2,"params":{"producto":"pizza"}}`}
	c = New(low, "m", cfg)
	got, err = c.Classify(context.Background(), "quiero pizza")
	if err != nil {
		t.Fatalf("Classify bajo umbral: %v", err)
	}
	if got.Intent != intents.ReservedUnknown || got.Params != nil {
		t.Errorf("bajo umbral debía dar desconocido sin params: %+v", got)
	}
}

func TestClassifyBadJSONFallsBackToUnknown(t *testing.T) {
	cfg := loadFixture(t)
	c := New(&stubLLM{content: "no soy json"}, "m", cfg)
	got, err := c.Classify(context.Background(), "hola")
	if err != nil {
		t.Fatalf("un JSON ilegible no debe ser error: %v", err)
	}
	if got.Intent != intents.ReservedUnknown {
		t.Errorf("fallback esperaba desconocido, got %q", got.Intent)
	}
}

func TestClassifySetsThinkOnlyWhenSupported(t *testing.T) {
	cfg := loadFixture(t)
	content := `{"intent":"horario_atencion","confidence":0.9,"params":{}}`

	withThink := &stubLLM{content: content, thinking: true}
	if _, err := New(withThink, "qwen3", cfg).Classify(context.Background(), "a que hora abren"); err != nil {
		t.Fatalf("Classify con thinking: %v", err)
	}
	if withThink.lastReq.Think == nil || *withThink.lastReq.Think != false {
		t.Error("con capability thinking debe enviar think:false")
	}

	noThink := &stubLLM{content: content, thinking: false}
	if _, err := New(noThink, "gemma", cfg).Classify(context.Background(), "a que hora abren"); err != nil {
		t.Fatalf("Classify sin thinking: %v", err)
	}
	if noThink.lastReq.Think != nil {
		t.Error("sin capability thinking NO debe enviar el campo think")
	}
	temp, ok := noThink.lastReq.Options["temperature"].(float64)
	if !ok || temp != 0.1 {
		t.Errorf("temperatura = %v (ok=%v), se esperaba 0.1", temp, ok)
	}
}

func TestReloadSwapsConfig(t *testing.T) {
	cfg := loadFixture(t)
	c := New(&stubLLM{content: `{"intent":"desconocido","confidence":0}`}, "m", cfg)

	// Config nueva mínima con un intent distinto.
	newCfg, err := intents.ParseAndValidate([]byte(`{"version":"2","intents":[{"name":"saludar","descripcion":"saluda","ejemplos":[{"mensaje":"hola"}]}]}`))
	if err != nil {
		t.Fatalf("config nueva inválida: %v", err)
	}
	c.Reload(newCfg)
	if !strings.Contains(c.prompt, "saludar") || strings.Contains(c.prompt, "crear_pedido") {
		t.Error("Reload no regeneró el prompt con la config nueva")
	}
}

// --- helpers ---

func equalMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
