// Package provider — provider factory with model-based auto-detection.
// Inspired by picoclaw's CreateProvider pattern: detect the right backend
// from the model name so users only need to set a model and API key.
package provider

import (
	"fmt"
	"log/slog"
	"strings"
)

// ProviderConfig holds the configuration for creating an LLM provider.
type ProviderConfig struct {
	// Provider name: "anthropic", "openai", "groq", "deepseek", "together",
	// "ollama", "openrouter", or any OpenAI-compatible endpoint.
	// If empty, auto-detected from the model name.
	Provider string `json:"provider" yaml:"provider"`

	// API key for the provider. Not needed for local providers (Ollama, vLLM).
	APIKey string `json:"api_key" yaml:"api_key"`

	// Custom API endpoint. Required for Ollama/vLLM, optional for others.
	Endpoint string `json:"endpoint" yaml:"endpoint"`

	// Model name. Used for auto-detection if Provider is empty.
	Model string `json:"model" yaml:"model"`

	// Maximum tokens for responses.
	MaxTokens int `json:"max_tokens" yaml:"max_tokens"`
}

// KnownProvider maps provider names to their default endpoints.
var KnownProviders = map[string]string{
	"anthropic":  "", // Uses its own API format, not OpenAI-compat.
	"openai":     "https://api.openai.com/v1/chat/completions",
	"grok":       "https://api.x.ai/v1/chat/completions",
	"gemini":     "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions", // Gemini OpenAI-compat endpoint
	"deepseek":   "https://api.deepseek.com/v1/chat/completions",
	"together":   "https://api.together.xyz/v1/chat/completions",
	"openrouter": "https://openrouter.ai/api/v1/chat/completions",
	"ollama":     "http://localhost:11434/v1/chat/completions",
	"vllm":       "http://localhost:8000/v1/chat/completions",
	"lmstudio":   "http://localhost:1234/v1/chat/completions",
}

// CreateProvider builds the right LLM provider from configuration.
// If provider is not specified, it auto-detects from the model name.
func CreateProvider(cfg ProviderConfig, logger *slog.Logger) (Provider, error) {
	providerName := cfg.Provider

	// Auto-detect provider from model name if not specified.
	if providerName == "" && cfg.Model != "" {
		providerName = detectProvider(cfg.Model)
	}

	// Default to anthropic if nothing specified.
	if providerName == "" {
		providerName = "anthropic"
	}

	logger.Info("creating LLM provider", "provider", providerName, "model", cfg.Model)

	switch providerName {
	case "anthropic":
		if cfg.APIKey == "" {
			return nil, fmt.Errorf("provider.Create: ANTHROPIC_API_KEY is required for Anthropic provider")
		}
		var opts []AnthropicOption
		if cfg.Model != "" {
			opts = append(opts, WithModel(cfg.Model))
		}
		if cfg.MaxTokens > 0 {
			opts = append(opts, WithMaxTokens(cfg.MaxTokens))
		}
		return NewAnthropicProvider(cfg.APIKey, logger, opts...), nil

	default:
		// Everything else uses the OpenAI-compatible provider.
		endpoint := cfg.Endpoint
		if endpoint == "" {
			if ep, ok := KnownProviders[providerName]; ok && ep != "" {
				endpoint = ep
			} else {
				return nil, fmt.Errorf("provider.Create: unknown provider %q and no endpoint specified", providerName)
			}
		}

		var opts []OpenAICompatOption
		opts = append(opts, WithOAIEndpoint(endpoint))
		opts = append(opts, WithOAIName(providerName))
		if cfg.Model != "" {
			opts = append(opts, WithOAIModel(cfg.Model))
		}
		if cfg.MaxTokens > 0 {
			opts = append(opts, WithOAIMaxTokens(cfg.MaxTokens))
		}
		return NewOpenAICompatProvider(cfg.APIKey, logger, opts...), nil
	}
}

// detectProvider guesses the provider from a model name string.
func detectProvider(model string) string {
	m := strings.ToLower(model)

	switch {
	case strings.HasPrefix(m, "claude") || strings.Contains(m, "anthropic"):
		return "anthropic"
	case strings.HasPrefix(m, "gpt-") || strings.HasPrefix(m, "o1") || strings.HasPrefix(m, "o3"):
		return "openai"
	case strings.HasPrefix(m, "grok"):
		return "grok"
	case strings.HasPrefix(m, "gemini") || strings.Contains(m, "google"):
		return "gemini"
	case strings.HasPrefix(m, "deepseek"):
		return "deepseek"
	case strings.Contains(m, "together/"):
		return "together"
	case strings.Contains(m, "openrouter/"):
		return "openrouter"
	case isOllamaModel(m):
		return "ollama"
	default:
		return "" // Caller will default to anthropic.
	}
}

// isOllamaModel detects if a model name is likely an Ollama local model.
// This includes popular small models suitable for Raspberry Pi.
func isOllamaModel(model string) bool {
	// Explicit ollama prefix
	if strings.Contains(model, "ollama/") {
		return true
	}

	// Small models ideal for Raspberry Pi (1-8B parameters)
	piModels := []string{
		// Meta Llama family
		"llama3", "llama2", "llama-3", "llama-2",
		// Google Gemma family
		"gemma", "gemma2", "gemma:2b", "gemma:7b",
		// Microsoft Phi family (excellent for Pi)
		"phi", "phi3", "phi-3", "phi3.5", "phi-3.5",
		// Mistral family
		"mistral", "mistral-7b", "mixtral",
		// TinyLlama (very small, fast on Pi)
		"tinyllama", "tiny-llama",
		// Qwen family (good multilingual)
		"qwen", "qwen2", "qwen:0.5b", "qwen:1.8b",
		// StableLM (compact)
		"stablelm", "stable-lm",
		// Orca (fine-tuned for reasoning)
		"orca", "orca-mini",
		// CodeLlama (for coding tasks)
		"codellama", "code-llama",
		// Neural Chat (Intel optimized)
		"neural-chat",
		// Dolphin (uncensored variants)
		"dolphin",
		// Vicuna
		"vicuna",
		// OpenHermes
		"openhermes",
		// Starling
		"starling",
		// Zephyr
		"zephyr",
		// NVIDIA Nemotron (user requested)
		"nemotron", "nemotron-mini",
	}

	for _, pm := range piModels {
		if strings.HasPrefix(model, pm) || strings.Contains(model, pm) {
			return true
		}
	}

	return false
}
