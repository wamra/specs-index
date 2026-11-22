package provider

import "strings"

// ModelDefaults are the prompt prefixes and vector size of a known embedding
// model. Dims 0 means "auto-detect".
type ModelDefaults struct {
	DocPrefix, QueryPrefix string
	Dims                   int
}

// Cohere embed models take an input_type instead of a text prefix.
const (
	CohereDocType   = "search_document"
	CohereQueryType = "search_query"
)

// Prefixes of embeddinggemma (also the historical defaults).
const (
	GemmaDocPrefix   = "title: none | text: "
	GemmaQueryPrefix = "task: search result | query: "
)

// KnownEmbedModel returns the defaults for a model name. Names are matched
// case-insensitively after dropping an "org/" path and a ":tag".
func KnownEmbedModel(model string) (ModelDefaults, bool) {
	m := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	if i := strings.Index(m, ":"); i >= 0 {
		m = m[:i]
	}
	switch {
	case strings.HasPrefix(m, "embeddinggemma"):
		return ModelDefaults{GemmaDocPrefix, GemmaQueryPrefix, 768}, true
	case strings.HasPrefix(m, "nomic-embed-text"), strings.HasPrefix(m, "text-embedding-nomic-embed-text"):
		return ModelDefaults{"search_document: ", "search_query: ", 768}, true
	case strings.HasPrefix(m, "all-minilm"):
		return ModelDefaults{"", "", 384}, true
	case strings.HasPrefix(m, "bge-"):
		return ModelDefaults{"", "Represent this sentence for searching relevant passages: ", 0}, true
	case strings.HasPrefix(m, "text-embedding-3-small"):
		return ModelDefaults{"", "", 1536}, true
	case m == "voyage-3":
		return ModelDefaults{"", "", 1024}, true
	case m == "voyage-3-lite":
		return ModelDefaults{"", "", 512}, true
	case strings.HasPrefix(m, "amazon.titan-embed-text-v2"):
		// 1024 is Titan's default size; embed.dims may pick 256 or 512.
		return ModelDefaults{"", "", 1024}, true
	case strings.HasPrefix(m, "cohere.embed-"):
		// The "prefixes" are Cohere input_type values, not text.
		return ModelDefaults{CohereDocType, CohereQueryType, 1024}, true
	}
	return ModelDefaults{}, false
}
