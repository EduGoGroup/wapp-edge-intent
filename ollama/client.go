// Package ollama es un cliente HTTP mínimo de la API REST local de Ollama
// (por defecto http://127.0.0.1:11434), usado por el clasificador de intenciones
// del Edge. Solo cubre lo que el clasificador necesita: inferencia con salida
// forzada por JSON Schema, detección de capabilities y sondeo de salud.
//
// Cambiar de modelo es solo cambiar el campo Model de cada petición: Ollama
// carga/descarga modelos en caliente, no hay que reiniciar nada.
package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"
)

// Client habla con la API REST local de Ollama. Es seguro para uso concurrente:
// la caché de capabilities está protegida por su mutex.
type Client struct {
	baseURL string
	http    *http.Client

	mu   sync.Mutex
	caps map[string][]string // caché modelo → capabilities (/api/show)
}

// New crea un cliente contra baseURL (p. ej. "http://127.0.0.1:11434").
//
// El http.Client NO tiene timeout global a propósito: la primera carga de un
// modelo en frío puede tardar 3–7 s. El plazo lo pone el context de cada
// llamada (context.WithTimeout en el caller).
func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{},
		caps:    map[string][]string{},
	}
}

// Message es un turno de conversación (system | user | assistant).
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest son los parámetros de una inferencia. Format, Think y Options son
// opcionales; el clasificador los fija (JSON Schema, think:false, temperatura).
type ChatRequest struct {
	Model    string
	Messages []Message
	Format   json.RawMessage // "json" o un JSON Schema; fuerza la forma de la salida
	// Think solo es válido en modelos con capability "thinking" (ej. qwen3);
	// enviarlo a otros modelos es error de Ollama, por eso es puntero.
	Think   *bool
	Options map[string]any
}

// chatRequestWire es la forma exacta que espera /api/chat.
type chatRequestWire struct {
	Model    string          `json:"model"`
	Messages []Message       `json:"messages"`
	Stream   bool            `json:"stream"`
	Format   json.RawMessage `json:"format,omitempty"`
	Options  map[string]any  `json:"options,omitempty"`
	Think    *bool           `json:"think,omitempty"`
}

// ChatResponse es la respuesta completa de una inferencia sin streaming.
type ChatResponse struct {
	Model   string  `json:"model"`
	Message Message `json:"message"`
	Done    bool    `json:"done"`

	// Métricas de Ollama (nanosegundos / tokens) para medir factibilidad en Edge.
	TotalDuration   int64 `json:"total_duration"`
	LoadDuration    int64 `json:"load_duration"`
	PromptEvalCount int   `json:"prompt_eval_count"`
	EvalCount       int   `json:"eval_count"`
	EvalDuration    int64 `json:"eval_duration"`
}

// Content es el texto generado por el modelo.
func (r *ChatResponse) Content() string { return r.Message.Content }

// Metrics resume el costo de una inferencia, para evaluar hardware de Edge.
type Metrics struct {
	TotalMs      int64   `json:"total_ms"`
	LoadMs       int64   `json:"load_ms"`
	PromptTokens int     `json:"prompt_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TokensPerSec float64 `json:"tokens_per_sec"`
}

// Metrics extrae las métricas de la respuesta en unidades legibles.
func (r *ChatResponse) Metrics() Metrics {
	m := Metrics{
		TotalMs:      r.TotalDuration / int64(time.Millisecond),
		LoadMs:       r.LoadDuration / int64(time.Millisecond),
		PromptTokens: r.PromptEvalCount,
		OutputTokens: r.EvalCount,
	}
	if r.EvalDuration > 0 {
		m.TokensPerSec = float64(r.EvalCount) / (float64(r.EvalDuration) / float64(time.Second))
	}
	return m
}

// Chat hace una inferencia completa (sin streaming). Si req.Format no es nil,
// fuerza la salida al esquema dado — clave para el modo clasificador. El plazo
// lo gobierna ctx.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	body, err := json.Marshal(chatRequestWire{
		Model:    req.Model,
		Messages: req.Messages,
		Stream:   false,
		Format:   req.Format,
		Options:  req.Options,
		Think:    req.Think,
	})
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama no responde en %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, rerr := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if rerr != nil {
			msg = []byte(rerr.Error())
		}
		return nil, fmt.Errorf("ollama devolvió %d: %s", resp.StatusCode, msg)
	}

	var out ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("ollama: respuesta ilegible: %w", err)
	}
	return &out, nil
}

// SupportsThinking consulta (y cachea por modelo) si el modelo tiene la
// capability "thinking". Un fallo de red se cachea como "no la tiene": es la
// respuesta segura (no enviar think a un modelo desconocido).
func (c *Client) SupportsThinking(ctx context.Context, model string) bool {
	c.mu.Lock()
	caps, ok := c.caps[model]
	c.mu.Unlock()
	if !ok {
		caps = c.fetchCapabilities(ctx, model)
		c.mu.Lock()
		c.caps[model] = caps
		c.mu.Unlock()
	}
	return slices.Contains(caps, "thinking")
}

// fetchCapabilities pregunta a /api/show las capabilities de un modelo.
func (c *Client) fetchCapabilities(ctx context.Context, model string) []string {
	body, err := json.Marshal(map[string]string{"model": model})
	if err != nil {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/show", bytes.NewReader(body))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var out struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil
	}
	return out.Capabilities
}

// ModelInfo describe un modelo de chat instalado en Ollama.
type ModelInfo struct {
	Name          string `json:"name"`
	ParameterSize string `json:"parameter_size"`
	SizeBytes     int64  `json:"size_bytes"`
}

// ListModels devuelve los modelos de chat instalados (excluye los de embeddings).
func (c *Client) ListModels(ctx context.Context) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama no responde en %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()

	var raw struct {
		Models []struct {
			Name    string `json:"name"`
			Size    int64  `json:"size"`
			Details struct {
				ParameterSize string `json:"parameter_size"`
			} `json:"details"`
			Capabilities []string `json:"capabilities"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	models := make([]ModelInfo, 0, len(raw.Models))
	for _, m := range raw.Models {
		if isEmbeddingOnly(m.Capabilities) {
			continue
		}
		models = append(models, ModelInfo{
			Name:          m.Name,
			ParameterSize: m.Details.ParameterSize,
			SizeBytes:     m.Size,
		})
	}
	return models, nil
}

// Health sondea si Ollama responde (GET /api/tags). Devuelve error si no está
// alcanzable o responde con estado != 200. El caller acota el plazo con ctx.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/tags", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("ollama no responde en %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama en %s devolvió estado %d", c.baseURL, resp.StatusCode)
	}
	return nil
}

// isEmbeddingOnly reporta si un modelo solo sirve para embeddings (sin chat).
func isEmbeddingOnly(caps []string) bool {
	hasEmbedding := false
	for _, c := range caps {
		if c == "completion" {
			return false
		}
		if c == "embedding" {
			hasEmbedding = true
		}
	}
	return hasEmbedding
}
