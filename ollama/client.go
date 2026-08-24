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

// KeepAliveForever es el valor de keep_alive que le pide a Ollama mantener el
// modelo cargado indefinidamente. Ollama trata CUALQUIER número negativo como
// "para siempre"; -1 es la forma canónica de escribirlo.
const KeepAliveForever = -1

// DefaultKeepAliveSeconds es el keep_alive que este módulo recomienda al Edge:
// para siempre. No lo aplica nadie por su cuenta —ChatRequest.KeepAlive sigue
// siendo opcional— pero da un nombre al número para que el consumidor no lo
// escriba a ciegas en su configuración. Es una constante aparte de
// KeepAliveForever a propósito: aquella dice qué SIGNIFICA -1 para Ollama, esta
// dice qué ELEGIMOS nosotros, y mañana puede bajar a un valor finito sin tocar
// la otra.
const DefaultKeepAliveSeconds = KeepAliveForever

// KeepAliveSeconds envuelve s para poder asignarlo a ChatRequest.KeepAlive: Go
// no deja tomar la dirección de un literal, y sin esto cada llamador tendría que
// declararse una variable temporal.
func KeepAliveSeconds(s int) *int { return &s }

// ChatRequest son los parámetros de una inferencia. Format, Think, Options y
// KeepAlive son opcionales; el clasificador los fija (JSON Schema, think:false,
// temperatura).
type ChatRequest struct {
	Model    string
	Messages []Message
	Format   json.RawMessage // "json" o un JSON Schema; fuerza la forma de la salida
	// Think solo es válido en modelos con capability "thinking" (ej. qwen3);
	// enviarlo a otros modelos es error de Ollama, por eso es puntero.
	Think   *bool
	Options map[string]any
	// KeepAlive, si no es nil, dice cuántos SEGUNDOS debe Ollama mantener el
	// modelo en memoria tras responder; KeepAliveForever (-1) = para siempre.
	// nil = no se manda el campo, y entonces manda lo que tenga configurado el
	// servidor (por defecto 5 minutos, o lo que diga OLLAMA_KEEP_ALIVE).
	//
	// POR QUÉ IMPORTA: cuando el runner muere no se lleva solo el modelo, se
	// lleva la CACHÉ DE PREFIJOS con él. El siguiente mensaje paga carga del
	// modelo (39 s medidos el 2026-08-23) MÁS el prefill en frío del prompt
	// entero. En el VPS de UAT hoy eso lo tapa OLLAMA_KEEP_ALIVE=-1 en el env de
	// la unidad, pero eso es una propiedad del servidor de ESA máquina: en el
	// equipo de un cliente no hay nadie que la ponga, y el campo sí viaja con
	// cada petición.
	//
	// ES PUNTERO, NO int: con un int desnudo el valor cero —que para Ollama
	// significa "descarga el modelo AHORA MISMO"— sería indistinguible de "no lo
	// fijé". El puntero separa las dos cosas, y omitempty sobre un puntero omite
	// solo cuando es nil (un puntero a 0 sí se serializa).
	//
	// NO VA DENTRO DE Options: keep_alive es un campo de primer nivel de
	// /api/chat. Metido en options, Ollama lo IGNORA en silencio —las claves
	// desconocidas de options no dan error— y el modelo seguiría muriéndose sin
	// que nada lo delate.
	KeepAlive *int
}

// chatRequestWire es la forma exacta que espera /api/chat.
type chatRequestWire struct {
	Model    string          `json:"model"`
	Messages []Message       `json:"messages"`
	Stream   bool            `json:"stream"`
	Format   json.RawMessage `json:"format,omitempty"`
	Options  map[string]any  `json:"options,omitempty"`
	Think    *bool           `json:"think,omitempty"`
	// keep_alive viaja como NÚMERO (segundos), no como la cadena tipo "5m" que
	// Ollama también acepta: el número lo lee directo —negativo = para siempre—
	// mientras que la cadena pasa por time.ParseDuration, y una cadena mal
	// escrita es un 400 que solo se ve en campo. Además el valor nace de una
	// configuración, donde un entero es lo natural.
	KeepAlive *int `json:"keep_alive,omitempty"`
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
	// PromptEvalDuration es el PREFILL: lo que costó digerir el prompt de
	// entrada (sus PromptEvalCount tokens) ANTES de generar el primer token de
	// salida. Es el gemelo de EvalDuration, que mide lo contrario —la
	// GENERACIÓN de los EvalCount tokens de respuesta—, y ninguno de los dos
	// incluye LoadDuration (cargar el modelo del disco).
	//
	// Ollama siempre lo ha devuelto y nosotros lo tirábamos, y por eso la
	// latencia se publicaba como UN SOLO NÚMERO que mezcla dos regímenes que se
	// diferencian en un orden de magnitud: con el prefijo FRÍO el prefill cuesta
	// ~21,6 ms por token, con el prefijo CALIENTE (la caché de prefijos del
	// runner viva) baja a 0,1–1,2 s el prompt entero. Ese número mezclado es el
	// que dejó dos p50 irreconciliables en el repo, ~20 s en el informe de
	// diseño contra 8,1 s en campo: la diferencia no era el modelo ni la
	// máquina, era el calor del prefijo.
	PromptEvalDuration int64 `json:"prompt_eval_duration"`
	EvalCount          int   `json:"eval_count"`
	EvalDuration       int64 `json:"eval_duration"`
}

// Content es el texto generado por el modelo.
func (r *ChatResponse) Content() string { return r.Message.Content }

// Metrics resume el costo de una inferencia, para evaluar hardware de Edge.
type Metrics struct {
	TotalMs int64 `json:"total_ms"`
	LoadMs  int64 `json:"load_ms"`
	// PromptMs es el prefill en milisegundos (ver ChatResponse.PromptEvalDuration).
	// Va aquí y no solo en la respuesta cruda porque Metrics es LA vista que
	// consume el Edge: si el prefill fuera el único dato que hay que ir a buscar
	// a ChatResponse, el mismo log acabaría sumando números de dos sitios
	// distintos y en dos unidades distintas.
	PromptMs     int64   `json:"prompt_ms"`
	PromptTokens int     `json:"prompt_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TokensPerSec float64 `json:"tokens_per_sec"`
}

// Metrics extrae las métricas de la respuesta en unidades legibles.
func (r *ChatResponse) Metrics() Metrics {
	m := Metrics{
		TotalMs:      r.TotalDuration / int64(time.Millisecond),
		LoadMs:       r.LoadDuration / int64(time.Millisecond),
		PromptMs:     r.PromptEvalDuration / int64(time.Millisecond),
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
		Model:     req.Model,
		Messages:  req.Messages,
		Stream:    false,
		Format:    req.Format,
		Options:   req.Options,
		Think:     req.Think,
		KeepAlive: req.KeepAlive,
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
