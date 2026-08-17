package classifier

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
// resp es una plantilla opcional para las métricas de la respuesta (durations y
// contadores de tokens); su Message lo pisa siempre content.
type stubLLM struct {
	content  string
	thinking bool
	resp     ollama.ChatResponse
	lastReq  ollama.ChatRequest
}

func (s *stubLLM) Chat(_ context.Context, req ollama.ChatRequest) (*ollama.ChatResponse, error) {
	s.lastReq = req
	out := s.resp
	out.Message = ollama.Message{Role: "assistant", Content: s.content}
	return &out, nil
}

// userContent devuelve el texto que se le mandó al modelo como turno de usuario.
func (s *stubLLM) userContent(t *testing.T) string {
	t.Helper()
	if len(s.lastReq.Messages) != 2 {
		t.Fatalf("se esperaban 2 mensajes (system+user), hay %d", len(s.lastReq.Messages))
	}
	return s.lastReq.Messages[1].Content
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

// TestBuildSchemaBoundsConfidence fija el RANGO de `confidence` en la gramática.
//
// Sin minimum/maximum, `{"confidence":100}` es JSON perfectamente válido para este
// schema y el umbral de Classify (`out.Confidence < cfg.UmbralConfianza`) no puede
// atrapar nada: 100 < 0.6 es falso. No es teoría — medido contra qwen3:1.7b, que
// devolvió "horario_atencion" con confidence: 100 y pasó un umbral de 0.6.
//
// El test mira la GRAMÁTICA, no el parseo, porque hoy la gramática es la única
// defensa: parseClassification no satura ni rechaza un valor fuera de rango.
func TestBuildSchemaBoundsConfidence(t *testing.T) {
	cfg := loadFixture(t)
	var s struct {
		Properties struct {
			Confidence map[string]json.RawMessage `json:"confidence"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(buildSchema(cfg), &s); err != nil {
		t.Fatalf("schema ilegible: %v", err)
	}
	for _, k := range []string{"minimum", "maximum"} {
		if _, ok := s.Properties.Confidence[k]; !ok {
			t.Fatalf("confidence no declara %q: el umbral de Classify queda decorativo (100 < 0.6 es falso)", k)
		}
	}
	var lo, hi float64
	if err := json.Unmarshal(s.Properties.Confidence["minimum"], &lo); err != nil {
		t.Fatalf("minimum ilegible: %v", err)
	}
	if err := json.Unmarshal(s.Properties.Confidence["maximum"], &hi); err != nil {
		t.Fatalf("maximum ilegible: %v", err)
	}
	if lo != 0 || hi != 1 {
		t.Errorf("confidence acotada a [%v,%v], se esperaba [0,1] — el umbral compara probabilidades, no porcentajes", lo, hi)
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

// --- techo de entrada (T2.5) ---

func TestTruncateRunesCutsByRunesNotBytes(t *testing.T) {
	// "ñ" ocupa 2 bytes: recortar por bytes partiría el carácter por la mitad.
	s := strings.Repeat("ñ", 10)
	got, cut := truncateRunes(s, 4)
	if !cut {
		t.Error("se esperaba recorte")
	}
	if !utf8.ValidString(got) {
		t.Errorf("el recorte produjo UTF-8 inválido: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != 4 {
		t.Errorf("runas = %d, se esperaban 4", n)
	}
	if len(got) != 8 {
		t.Errorf("bytes = %d, se esperaban 8 (4 runas × 2 bytes) — ¿se cortó por bytes?", len(got))
	}
	if !strings.HasPrefix(s, got) {
		t.Error("el recorte debe ser prefijo exacto del original")
	}

	// Justo en el límite y por debajo: no se toca nada.
	if got, cut := truncateRunes(s, 10); cut || got != s {
		t.Errorf("un texto de exactamente el techo no debe truncarse (cut=%v)", cut)
	}
	if got, cut := truncateRunes("hola", DefaultMaxRunes); cut || got != "hola" {
		t.Errorf("un texto corto no debe truncarse (cut=%v)", cut)
	}
	// Defensa: techo no positivo ⇒ no se recorta (New nunca lo deja así).
	if got, cut := truncateRunes(s, 0); cut || got != s {
		t.Error("con techo 0 no debe recortar")
	}
}

func TestClassifyTruncatesInputAndMarksTruncado(t *testing.T) {
	cfg := loadFixture(t)
	content := `{"intent":"horario_atencion","confidence":0.9,"params":{}}`

	// Multi-byte justo en el corte: 25 runas "ñ" = 50 bytes.
	text := strings.Repeat("ñ", 50) + " a que hora abren"
	stub := &stubLLM{content: content}
	got, err := New(stub, "m", cfg, WithMaxRunes(25)).Classify(context.Background(), text)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	sent := stub.userContent(t)
	if n := utf8.RuneCountInString(sent); n != 25 {
		t.Errorf("se mandaron %d runas, se esperaban 25", n)
	}
	if len(sent) != 50 {
		t.Errorf("bytes mandados = %d, se esperaban 50 — el corte no fue por runas", len(sent))
	}
	if !utf8.ValidString(sent) {
		t.Errorf("se mandó UTF-8 inválido: %q", sent)
	}
	if !got.Truncado {
		t.Error("Truncado debe ser true cuando se recorta")
	}

	// Sin recorte: el texto viaja íntegro y Truncado es false.
	stub = &stubLLM{content: content}
	got, err = New(stub, "m", cfg).Classify(context.Background(), "a que hora abren")
	if err != nil {
		t.Fatalf("Classify sin truncado: %v", err)
	}
	if sent := stub.userContent(t); sent != "a que hora abren" {
		t.Errorf("el texto corto no debe alterarse, got %q", sent)
	}
	if got.Truncado {
		t.Error("Truncado debe ser false cuando no se recorta")
	}
}

// TestClassifyAppliesDefaultCeilingToHugeInput reproduce el criterio de T2.5: la
// entrada de ~65 KB que hoy abre el breaker se recorta al techo por defecto.
func TestClassifyAppliesDefaultCeilingToHugeInput(t *testing.T) {
	cfg := loadFixture(t)
	stub := &stubLLM{content: `{"intent":"desconocido","confidence":0}`}
	huge := strings.Repeat("a", 65*1024)

	got, err := New(stub, "m", cfg).Classify(context.Background(), huge)
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if n := utf8.RuneCountInString(stub.userContent(t)); n != DefaultMaxRunes {
		t.Errorf("se mandaron %d runas, se esperaba el techo por defecto (%d)", n, DefaultMaxRunes)
	}
	if !got.Truncado {
		t.Error("una entrada de 65 KB debe marcarse como truncada")
	}
}

// TestDefaultCeilingFitsTheContextWindow fija la ARITMÉTICA que justifica el techo
// de entrada (no el número por el número): el peor caso de prompt tiene que caber
// en DefaultNumCtx, y el techo tiene que seguir cubriendo el tráfico real.
//
// El peor caso lo marca el alfabeto más denso medido con qwen3:1.7b —el EMOJI, a
// 1,0 tokens/runa (español 0,257 · cirílico 0,390 · CJK 0,666)— más el prompt de
// sistema (911 tokens hoy; DefaultNumCtx anticipa hasta ~1500 con un contrato rico)
// más la respuesta (DefaultNumPredict). Con el techo viejo de 4000 esto NO cabía:
// el emoji medía 4.912 tokens de prompt_eval contra una ventana de 4.096, y Ollama
// descartaba el 58 % de la entrada en silencio.
//
// El otro motivo del techo, la LATENCIA (a 4000 runas la inferencia tardaba 32,6 s
// en español y 119,7 s en emoji, contra un plazo de 15 s en el worker), no se puede
// comprobar aquí: el plazo lo fija el consumidor y este paquete no lo conoce.
func TestDefaultCeilingFitsTheContextWindow(t *testing.T) {
	const (
		peorCasoTokPorRuna  = 1    // emoji: 1,0 tok/runa, el máximo medido
		systemPromptRicoTok = 1500 // el contrato rico que DefaultNumCtx anticipa
		loteRealMaxRunas    = 196  // lote más grande observado en tráfico real (8 mensajes)
	)

	peorCaso := DefaultMaxRunes*peorCasoTokPorRuna + systemPromptRicoTok + DefaultNumPredict
	if peorCaso > DefaultNumCtx {
		t.Errorf("peor caso %d tokens (%d runas × %d tok/runa + %d de prompt de sistema + %d de salida) no cabe en num_ctx %d: la entrada se descarta en silencio",
			peorCaso, DefaultMaxRunes, peorCasoTokPorRuna, systemPromptRicoTok, DefaultNumPredict, DefaultNumCtx)
	}

	// Suelo: el techo tampoco puede bajar tanto que recorte tráfico legítimo. Hoy es
	// 5× el lote real más grande medido; se exige 3× para dejar margen de calibración.
	if DefaultMaxRunes < 3*loteRealMaxRunas {
		t.Errorf("techo %d runas: por debajo de 3× el lote real más grande medido (%d runas), recortaría tráfico legítimo",
			DefaultMaxRunes, loteRealMaxRunas)
	}
}

func TestWithMaxRunesNonPositiveFallsBackToDefault(t *testing.T) {
	cfg := loadFixture(t)
	for _, n := range []int{0, -1} {
		stub := &stubLLM{content: `{"intent":"desconocido","confidence":0}`}
		c := New(stub, "m", cfg, WithMaxRunes(n))
		if c.maxRunes != DefaultMaxRunes {
			t.Errorf("WithMaxRunes(%d): maxRunes = %d, se esperaba %d", n, c.maxRunes, DefaultMaxRunes)
		}
		if _, err := c.Classify(context.Background(), strings.Repeat("a", DefaultMaxRunes+10)); err != nil {
			t.Fatalf("Classify: %v", err)
		}
		if got := utf8.RuneCountInString(stub.userContent(t)); got != DefaultMaxRunes {
			t.Errorf("WithMaxRunes(%d): se mandaron %d runas, se esperaba %d", n, got, DefaultMaxRunes)
		}
	}
}

// TestClassifySanitizesAgainstTheTruncatedText fija la DECISIÓN de T2.5: el
// allowlist semántico se aplica contra el texto TRUNCADO (lo que el modelo LEYÓ),
// no contra el original. Un valor que solo vive en la cola cortada no pudo salir
// de ahí: el modelo lo alucinó y coincidió por casualidad.
func TestClassifySanitizesAgainstTheTruncatedText(t *testing.T) {
	cfg := loadFixture(t)
	const head = "quiero una " // 11 runas
	text := head + "pizza margarita"
	content := `{"intent":"crear_pedido","confidence":0.95,"params":{"producto":"margarita"}}`

	// Con recorte: "margarita" queda fuera de lo que el modelo vio ⇒ se descarta.
	got, err := New(&stubLLM{content: content}, "m", cfg, WithMaxRunes(len(head))).
		Classify(context.Background(), text)
	if err != nil {
		t.Fatalf("Classify truncado: %v", err)
	}
	if !got.Truncado {
		t.Fatal("el caso exige que haya recorte")
	}
	if got.Params != nil {
		t.Errorf("un param que solo aparece en la cola cortada debe descartarse, got %v", got.Params)
	}

	// Sin recorte, el MISMO param sobrevive: el test mide el truncado, no el
	// sanitizador.
	got, err = New(&stubLLM{content: content}, "m", cfg).Classify(context.Background(), text)
	if err != nil {
		t.Fatalf("Classify completo: %v", err)
	}
	if got.Truncado {
		t.Fatal("sin recorte Truncado debe ser false")
	}
	if got.Params["producto"] != "margarita" {
		t.Errorf("sin recorte el param debía sobrevivir, got %v", got.Params)
	}
}

// --- opciones del modelo (T2.5) ---

func TestClassifySendsExplicitModelOptions(t *testing.T) {
	cfg := loadFixture(t)
	stub := &stubLLM{content: `{"intent":"desconocido","confidence":0}`}
	if _, err := New(stub, "m", cfg).Classify(context.Background(), "hola que tal"); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	want := map[string]any{
		"temperature": 0.1,
		"num_thread":  DefaultNumThread,
		"num_ctx":     DefaultNumCtx,
		"num_predict": DefaultNumPredict,
	}
	for k, v := range want {
		if got := stub.lastReq.Options[k]; got != v {
			t.Errorf("options[%q] = %v, se esperaba %v", k, got, v)
		}
	}
}

func TestWithLLMOptionsMergesAndKeepsTemperature(t *testing.T) {
	cfg := loadFixture(t)
	stub := &stubLLM{content: `{"intent":"desconocido","confidence":0}`}
	extra := map[string]any{"num_ctx": 8192, "seed": 7}

	c := New(stub, "m", cfg, WithLLMOptions(extra))
	// Mutar el mapa del caller después de construir NO debe afectar al clasificador.
	extra["seed"] = 99
	if _, err := c.Classify(context.Background(), "hola que tal"); err != nil {
		t.Fatalf("Classify: %v", err)
	}

	opts := stub.lastReq.Options
	if opts["temperature"] != 0.1 {
		t.Errorf("la temperatura debe SOBREVIVIR a la fusión, got %v", opts["temperature"])
	}
	if opts["num_thread"] != DefaultNumThread || opts["num_predict"] != DefaultNumPredict {
		t.Errorf("la fusión no debe borrar las opciones por defecto: %v", opts)
	}
	if opts["num_ctx"] != 8192 {
		t.Errorf("num_ctx = %v, el override del caller debe ganar (8192)", opts["num_ctx"])
	}
	if opts["seed"] != 7 {
		t.Errorf("seed = %v, se esperaba 7 (el mapa del caller debe copiarse)", opts["seed"])
	}
}

// --- métricas (T2.6) ---

func TestClassifyPopulatesMetricsEvenWhenDegraded(t *testing.T) {
	cfg := loadFixture(t)
	tmpl := ollama.ChatResponse{
		TotalDuration:   int64(1500 * time.Millisecond),
		LoadDuration:    int64(300 * time.Millisecond),
		PromptEvalCount: 120,
		EvalCount:       20,
		EvalDuration:    int64(time.Second), // 20 tok / 1 s
	}
	want := Metrics{TotalMs: 1500, LoadMs: 300, PromptTokens: 120, OutputTokens: 20, TokensPerSec: 20}

	cases := map[string]string{
		"clasificación normal": `{"intent":"horario_atencion","confidence":0.9,"params":{}}`,
		"JSON ilegible":        "no soy json",
		"bajo umbral":          `{"intent":"crear_pedido","confidence":0.1,"params":{}}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := New(&stubLLM{content: content, resp: tmpl}, "m", cfg).
				Classify(context.Background(), "a que hora abren")
			if err != nil {
				t.Fatalf("Classify: %v", err)
			}
			if got.Metrics != want {
				t.Errorf("Metrics = %+v, se esperaba %+v", got.Metrics, want)
			}
		})
	}
}

// TestClassifyOptionsTravelInTheRequestJSON cierra el criterio de T2.5 de punta a
// punta: contra un Ollama de mentira (httptest) y con el cliente HTTP real, las
// tres opciones tienen que aparecer EN EL JSON de la petición (no basta con que
// estén en la struct), y las métricas de la respuesta tienen que llegar arriba.
func TestClassifyOptionsTravelInTheRequestJSON(t *testing.T) {
	cfg := loadFixture(t)

	var gotOpts map[string]any
	var gotUser string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/show":
			if err := json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion"}}); err != nil {
				t.Errorf("encode /api/show: %v", err)
			}
		case "/api/chat":
			var body struct {
				Messages []ollama.Message `json:"messages"`
				Options  map[string]any   `json:"options"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("body ilegible: %v", err)
				return
			}
			gotOpts = body.Options
			if len(body.Messages) == 2 {
				gotUser = body.Messages[1].Content
			}
			if err := json.NewEncoder(w).Encode(ollama.ChatResponse{
				Message:         ollama.Message{Role: "assistant", Content: `{"intent":"horario_atencion","confidence":0.9,"params":{}}`},
				Done:            true,
				TotalDuration:   int64(2 * time.Second),
				LoadDuration:    int64(500 * time.Millisecond),
				PromptEvalCount: 300,
				EvalCount:       40,
				EvalDuration:    int64(2 * time.Second), // 20 tok/s
			}); err != nil {
				t.Errorf("encode /api/chat: %v", err)
			}
		default:
			t.Errorf("ruta inesperada: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	got, err := New(ollama.New(srv.URL), "qwen3:1.7b", cfg).
		Classify(context.Background(), "a que hora abren")
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if got.Intent != "horario_atencion" {
		t.Errorf("intent = %q", got.Intent)
	}
	if gotUser != "a que hora abren" {
		t.Errorf("texto de usuario = %q", gotUser)
	}
	// Los números del JSON llegan como float64 al decodificar en map[string]any.
	want := map[string]float64{
		"temperature": 0.1,
		"num_thread":  float64(DefaultNumThread),
		"num_ctx":     float64(DefaultNumCtx),
		"num_predict": float64(DefaultNumPredict),
	}
	for k, v := range want {
		val, ok := gotOpts[k].(float64)
		if !ok {
			t.Errorf("options[%q] no viajó en el JSON de la petición (got %v)", k, gotOpts[k])
			continue
		}
		if val != v {
			t.Errorf("options[%q] = %v, se esperaba %v", k, val, v)
		}
	}
	wantMetrics := Metrics{TotalMs: 2000, LoadMs: 500, PromptTokens: 300, OutputTokens: 40, TokensPerSec: 20}
	if got.Metrics != wantMetrics {
		t.Errorf("Metrics = %+v, se esperaba %+v", got.Metrics, wantMetrics)
	}
	if got.Truncado {
		t.Error("Truncado debe ser false para un mensaje corto")
	}
}

// TestParseClassificationIgnoresInjectedFields: los campos que añade el Edge no
// son escribibles desde la salida del modelo.
func TestParseClassificationIgnoresInjectedFields(t *testing.T) {
	out, ok := parseClassification(`{"intent":"ver_carrito","confidence":0.9,"truncado":true,"metrics":{"total_ms":999}}`)
	if !ok {
		t.Fatal("el JSON era válido")
	}
	if out.Truncado || out.Metrics != (Metrics{}) {
		t.Errorf("el modelo no debe poder escribir Truncado/Metrics: %+v", out)
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
