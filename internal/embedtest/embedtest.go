// Package embedtest is a fake embedding server for tests: it answers Ollama's
// /api/embed and the OpenAI-compatible /v1/embeddings, and texts become hashed
// bag-of-words vectors, so texts that share words are close.
package embedtest

import (
	"encoding/json"
	"hash/fnv"
	"math"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
)

var wordRe = regexp.MustCompile(`[a-z0-9]+`)

// Vector returns the unit-length hashed bag-of-words vector of text.
func Vector(text string, dims int) []float32 {
	v := make([]float32, dims)
	for _, w := range wordRe.FindAllString(strings.ToLower(text), -1) {
		h := fnv.New32a()
		h.Write([]byte(w))
		v[int(h.Sum32())%dims]++
	}
	var n float64
	for _, x := range v {
		n += float64(x * x)
	}
	if n == 0 {
		v[0] = 1
		return v
	}
	n = math.Sqrt(n)
	for i := range v {
		v[i] = float32(float64(v[i]) / n)
	}
	return v
}

// Server is a running fake Ollama.
type Server struct {
	*httptest.Server
	// Requests counts embedding calls (either API), Texts the texts embedded,
	// Hits every HTTP request of any kind that reached the server.
	Requests, Texts, Hits atomic.Int64
	// Down makes every request fail with HTTP 503.
	Down atomic.Bool
}

// New starts a fake Ollama producing vectors of the given size; models lists
// the names /api/tags reports.
func New(dims int, models ...string) *Server {
	s := &Server{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/embed", func(w http.ResponseWriter, r *http.Request) {
		if s.Down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		var req struct {
			Input []string `json:"input"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		s.Requests.Add(1)
		s.Texts.Add(int64(len(req.Input)))
		out := make([][]float32, len(req.Input))
		for i, t := range req.Input {
			out[i] = Vector(t, dims)
		}
		json.NewEncoder(w).Encode(map[string]any{"embeddings": out})
	})
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, r *http.Request) {
		var ms []map[string]string
		for _, m := range models {
			ms = append(ms, map[string]string{"name": m, "model": m})
		}
		json.NewEncoder(w).Encode(map[string]any{"models": ms})
	})
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		if s.Down.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		var req struct {
			Input []string `json:"input"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		s.Requests.Add(1)
		s.Texts.Add(int64(len(req.Input)))
		data := make([]map[string]any, len(req.Input))
		for i, t := range req.Input {
			data[i] = map[string]any{"index": i, "embedding": Vector(t, dims)}
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.Hits.Add(1)
		mux.ServeHTTP(w, r)
	}))
	return s
}
