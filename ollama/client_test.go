package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
