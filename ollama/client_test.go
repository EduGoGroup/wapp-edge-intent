package ollama

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync/atomic"
	"testing"
	"time"
)

func TestChatSendsRequestAndParsesResponse(t *testing.T) {
	var gotThink *bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("ruta inesperada: %s", r.URL.Path)
		}
		var req chatRequestWire
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("body ilegible: %v", err)
		}
		if req.Stream {
			t.Error("Chat no debe usar streaming")
		}
		gotThink = req.Think
		if err := json.NewEncoder(w).Encode(ChatResponse{
			Message:      Message{Role: "assistant", Content: `{"ok":true}`},
			Done:         true,
			EvalCount:    10,
			EvalDuration: int64(time.Second), // 10 tok / 1 s = 10 tok/s
		}); err != nil {
			t.Errorf("encode: %v", err)
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	f := false
	resp, err := c.Chat(context.Background(), ChatRequest{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hola"}},
		Think:    &f,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content() != `{"ok":true}` {
		t.Errorf("content = %q", resp.Content())
	}
	if gotThink == nil || *gotThink != false {
		t.Errorf("think no se propagó: %v", gotThink)
	}
	if m := resp.Metrics(); m.TokensPerSec != 10 {
		t.Errorf("tokens/s = %v, quiero 10", m.TokensPerSec)
	}
}

func TestChatNon200IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "modelo no encontrado", http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := New(srv.URL).Chat(context.Background(), ChatRequest{Model: "x"})
	if err == nil {
		t.Fatal("se esperaba error por estado 404")
	}
}

func TestChatHonorsContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(ChatResponse{Done: true}) //nolint:errcheck // el caller ya canceló por deadline
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := New(srv.URL).Chat(ctx, ChatRequest{Model: "x"}); err == nil {
		t.Fatal("se esperaba error por deadline del context")
	}
}

func TestSupportsThinkingCachesPerModel(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		if err := json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{"completion", "thinking"}}); err != nil {
			t.Errorf("encode: %v", err)
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	if !c.SupportsThinking(context.Background(), "qwen3") {
		t.Error("qwen3 debería soportar thinking")
	}
	// Segunda llamada: debe salir de la caché sin volver a pegarle a /api/show.
	c.SupportsThinking(context.Background(), "qwen3")
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("/api/show se llamó %d veces, se esperaba 1 (caché)", got)
	}
}

func TestSupportsThinkingFalseOnNetworkError(t *testing.T) {
	// baseURL a un puerto muerto: el fallo se cachea como "no soporta".
	c := New("http://127.0.0.1:1")
	if c.SupportsThinking(context.Background(), "m") {
		t.Error("un fallo de red debe reportar que NO soporta thinking")
	}
}

func TestListModelsExcludesEmbeddingOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(`{"models":[
			{"name":"qwen3:1.7b","size":100,"details":{"parameter_size":"1.7B"},"capabilities":["completion"]},
			{"name":"nomic-embed","size":50,"capabilities":["embedding"]}
		]}`)); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	defer srv.Close()

	models, err := New(srv.URL).ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 1 || models[0].Name != "qwen3:1.7b" {
		t.Errorf("modelos = %+v, se esperaba solo qwen3", models)
	}
}

func TestHealth(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(`{"models":[]}`)); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	defer ok.Close()
	if err := New(ok.URL).Health(context.Background()); err != nil {
		t.Errorf("Health verde debería pasar: %v", err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	if err := New(bad.URL).Health(context.Background()); err == nil {
		t.Error("Health debería fallar con estado 503")
	}
}

func TestIsEmbeddingOnly(t *testing.T) {
	cases := []struct {
		caps []string
		want bool
	}{
		{[]string{"completion"}, false},
		{[]string{"completion", "embedding"}, false},
		{[]string{"embedding"}, true},
		{nil, false},
	}
	for _, tc := range cases {
		if got := isEmbeddingOnly(tc.caps); got != tc.want {
			t.Errorf("isEmbeddingOnly(%v) = %v, quiero %v", tc.caps, got, tc.want)
		}
	}
}

// capturaWire hace una petición contra un servidor de mentira y devuelve el
// cuerpo que SALIÓ POR EL CABLE, decodificado como mapa de claves crudas.
//
// Decodificar a map[string]json.RawMessage y no a chatRequestWire es el punto
// entero de estos tests: contra la struct, un tag `json` equivocado —o
// ausente— se serializa y se vuelve a leer con el mismo error en los dos
// sentidos y el test pasa igual. El mapa solo sabe de claves JSON, que es lo
// único que Ollama mira.
func capturaWire(ctx context.Context, t *testing.T, req ChatRequest) map[string]json.RawMessage {
	t.Helper()

	var crudo []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("body ilegible: %v", err)
			return
		}
		crudo = b
		if err := json.NewEncoder(w).Encode(ChatResponse{Done: true}); err != nil {
			t.Errorf("encode: %v", err)
		}
	}))
	defer srv.Close()

	if _, err := New(srv.URL).Chat(ctx, req); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var wire map[string]json.RawMessage
	if err := json.Unmarshal(crudo, &wire); err != nil {
		t.Fatalf("el cuerpo enviado no es un objeto JSON: %v (%s)", err, crudo)
	}
	return wire
}

// clavesDe ordena las claves de un cuerpo para que el mensaje de un fallo diga
// qué SÍ se envió, no solo qué falta.
func clavesDe(wire map[string]json.RawMessage) []string {
	ks := make([]string, 0, len(wire))
	for k := range wire {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func TestChatKeepAliveEnElWire(t *testing.T) {
	casos := []struct {
		nombre string
		req    ChatRequest
		quiero string // "" = la clave NO debe aparecer en el JSON
	}{
		{"para siempre", ChatRequest{Model: "m", KeepAlive: KeepAliveSeconds(KeepAliveForever)}, "-1"},
		{"finito, en segundos", ChatRequest{Model: "m", KeepAlive: KeepAliveSeconds(300)}, "300"},
		// Cero NO es "no lo fijé": para Ollama significa descargar el modelo en
		// cuanto responda. Es la razón de que el campo sea puntero, así que se
		// testea que ese cero SÍ viaja.
		{"cero explícito", ChatRequest{Model: "m", KeepAlive: KeepAliveSeconds(0)}, "0"},
		// Y el caso que protege al consumidor de v0.2.0: quien no lo fija manda
		// exactamente el mismo cuerpo que antes de que este campo existiera.
		{"sin fijar", ChatRequest{Model: "m"}, ""},
	}

	for _, tc := range casos {
		t.Run(tc.nombre, func(t *testing.T) {
			wire := capturaWire(context.Background(), t, tc.req)
			raw, presente := wire["keep_alive"]

			if tc.quiero == "" {
				if presente {
					t.Errorf("keep_alive no debía salir y salió con valor %s", raw)
				}
				return
			}
			if !presente {
				t.Fatalf("keep_alive no salió en el JSON; claves enviadas = %v", clavesDe(wire))
			}
			if got := string(raw); got != tc.quiero {
				t.Errorf("keep_alive = %s, quiero %s", got, tc.quiero)
			}
		})
	}
}

// respuestaRealDeOllama es un cuerpo de /api/chat con la forma que devuelve
// Ollama de verdad, con los números de una inferencia con el PREFIJO FRÍO:
// 1.489 tokens de prompt a ~21,6 ms/token son los 32,2 s de prefill, y por
// encima 39,0 s de cargar el modelo. Los 1,2 s de generación —lo único que
// mide eval_duration— son el 1,7 % del total: de ahí que publicar un solo
// número no dijera nada.
const respuestaRealDeOllama = `{
  "model": "qwen3:1.7b",
  "created_at": "2026-08-23T22:41:07.812345Z",
  "message": {"role": "assistant", "content": "{\"intent\":\"pedido\"}"},
  "done_reason": "stop",
  "done": true,
  "total_duration": 72385010000,
  "load_duration": 39012440000,
  "prompt_eval_count": 1489,
  "prompt_eval_duration": 32162400000,
  "eval_count": 42,
  "eval_duration": 1204500000
}`

func TestChatResponseDeserializaPromptEvalDuration(t *testing.T) {
	var resp ChatResponse
	if err := json.Unmarshal([]byte(respuestaRealDeOllama), &resp); err != nil {
		t.Fatalf("respuesta ilegible: %v", err)
	}

	if resp.PromptEvalDuration != 32162400000 {
		t.Errorf("PromptEvalDuration = %d ns, quiero 32162400000 (¿el tag json es prompt_eval_duration?)", resp.PromptEvalDuration)
	}
	// El prefill se lee aparte de la generación: son los dos regímenes que el
	// número único mezclaba.
	if resp.EvalDuration != 1204500000 {
		t.Errorf("EvalDuration = %d ns, quiero 1204500000", resp.EvalDuration)
	}

	m := resp.Metrics()
	if m.PromptMs != 32162 {
		t.Errorf("Metrics().PromptMs = %d, quiero 32162", m.PromptMs)
	}
	if m.TotalMs != 72385 || m.LoadMs != 39012 || m.PromptTokens != 1489 || m.OutputTokens != 42 {
		t.Errorf("el resto de Metrics cambió: %+v", m)
	}
}
