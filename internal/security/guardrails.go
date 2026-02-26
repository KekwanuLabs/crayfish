// Package security implements identity, sessions, and security guardrails.
package security

import (
	"regexp"
	"strings"
)

// Guardrails provides prompt injection detection and response sanitization.
type Guardrails struct {
	// Patterns that indicate prompt injection attempts.
	injectionPatterns []*regexp.Regexp

	// Patterns for sensitive data that should never be revealed.
	sensitivePatterns []*regexp.Regexp
}

// NewGuardrails creates a guardrails instance with default patterns.
func NewGuardrails() *Guardrails {
	g := &Guardrails{}

	// Prompt injection patterns — attempts to extract system info or override behavior.
	injectionStrings := []string{
		// System prompt extraction
		`(?i)ignore\s+(all\s+)?(previous|prior|above)\s+(instructions?|prompts?|rules?)`,
		`(?i)what\s+(is|are)\s+your\s+(system\s+)?(prompt|instructions?|rules?)`,
		`(?i)show\s+(me\s+)?(your\s+)?(system\s+)?(prompt|instructions?|rules?)`,
		`(?i)print\s+(your\s+)?(system\s+)?(prompt|instructions?)`,
		`(?i)reveal\s+(your\s+)?(system\s+)?(prompt|instructions?|rules?)`,
		`(?i)output\s+(your\s+)?(system\s+)?(prompt|instructions?)`,
		`(?i)display\s+(your\s+)?(system\s+)?(prompt|instructions?)`,
		`(?i)repeat\s+(your\s+)?(system\s+)?(prompt|instructions?)\s+(back|verbatim)`,
		`(?i)what\s+were\s+you\s+told`,
		`(?i)initial\s+instructions?`,

		// Tool/capability enumeration
		`(?i)list\s+(all\s+)?(your\s+)?(tools?|capabilities|functions?|commands?)`,
		`(?i)what\s+(tools?|capabilities|functions?)\s+(do\s+you\s+have|can\s+you)`,
		`(?i)enumerate\s+(your\s+)?(tools?|capabilities)`,

		// Token/credential extraction
		`(?i)show\s+(me\s+)?(your\s+)?(api\s+)?keys?`,
		`(?i)what\s+(is|are)\s+your\s+(api\s+)?keys?`,
		`(?i)reveal\s+(your\s+)?(api\s+)?(keys?|tokens?|credentials?)`,
		`(?i)print\s+(your\s+)?(api\s+)?(keys?|tokens?)`,
		`(?i)output\s+(the\s+)?(api\s+)?(keys?|tokens?)`,

		// Jailbreak attempts
		`(?i)you\s+are\s+now\s+(a\s+)?DAN`,
		`(?i)developer\s+mode\s+(enabled?|activate)`,
		`(?i)ignore\s+safety`,
		`(?i)bypass\s+(your\s+)?(safety|security|restrictions?)`,
		`(?i)pretend\s+(you\s+)?(have\s+)?no\s+(restrictions?|limits?)`,
		`(?i)act\s+as\s+if\s+(you\s+)?(have\s+)?no\s+(rules?|restrictions?)`,

		// Roleplay exploitation
		`(?i)roleplay\s+as\s+(a\s+)?(system|admin|root)`,
		`(?i)pretend\s+to\s+be\s+(a\s+)?(system|admin)`,
		`(?i)you\s+are\s+(a\s+)?(system|admin)\s+(with\s+)?(full\s+)?access`,

		// Hidden instruction injection
		`(?i)\[SYSTEM\]`,
		`(?i)\[INST\]`,
		`(?i)<\|system\|>`,
		`(?i)<\|assistant\|>`,
		`(?i)<<SYS>>`,
	}

	for _, pattern := range injectionStrings {
		g.injectionPatterns = append(g.injectionPatterns, regexp.MustCompile(pattern))
	}

	// Sensitive patterns — things that should never appear in responses.
	sensitiveStrings := []string{
		`(?i)sk-ant-[a-zA-Z0-9\-_]{20,}`, // Anthropic API keys
		`(?i)sk-[a-zA-Z0-9]{20,}`,        // OpenAI API keys
		`(?i)xai-[a-zA-Z0-9]{20,}`,       // xAI API keys
		`(?i)CRAYFISH_API_KEY\s*=\s*\S+`, // Env var leaks
		`(?i)CRAYFISH_TELEGRAM_TOKEN\s*=\s*\S+`,
		`(?i)CRAYFISH_GMAIL_APP_PASSWORD\s*=\s*\S+`,
		`(?i)BEGIN\s+(RSA\s+)?PRIVATE\s+KEY`, // Private keys
		`(?i)password\s*[:=]\s*['"]\S+['"]`,  // Inline passwords
	}

	for _, pattern := range sensitiveStrings {
		g.sensitivePatterns = append(g.sensitivePatterns, regexp.MustCompile(pattern))
	}

	return g
}

// InjectionAttempt represents a detected prompt injection.
type InjectionAttempt struct {
	Type        string // "extraction", "jailbreak", "enumeration", "injection"
	Pattern     string // The pattern that matched
	Confidence  string // "high", "medium", "low"
	Explanation string // Human-readable explanation
}

// CheckInput analyzes user input for prompt injection attempts.
// Returns nil if the input appears safe.
func (g *Guardrails) CheckInput(input string) *InjectionAttempt {
	normalized := strings.ToLower(strings.TrimSpace(input))

	for _, pattern := range g.injectionPatterns {
		if pattern.MatchString(normalized) {
			return &InjectionAttempt{
				Type:        classifyInjection(pattern.String()),
				Pattern:     pattern.String(),
				Confidence:  "high",
				Explanation: "Input matches known prompt injection pattern",
			}
		}
	}

	// Heuristic checks for suspicious patterns.
	suspiciousIndicators := 0

	// Multiple newlines followed by instructions (hidden prompt injection).
	if strings.Contains(input, "\n\n\n") || strings.Contains(input, "---\n") {
		suspiciousIndicators++
	}

	// Contains what looks like JSON/XML system tags.
	if strings.Contains(normalized, `"role":`) || strings.Contains(normalized, `<system>`) {
		suspiciousIndicators++
	}

	// Very long input with encoded content.
	if len(input) > 5000 && (strings.Contains(input, "base64") || strings.Count(input, "=") > 50) {
		suspiciousIndicators++
	}

	if suspiciousIndicators >= 2 {
		return &InjectionAttempt{
			Type:        "suspicious",
			Confidence:  "medium",
			Explanation: "Input contains multiple suspicious patterns",
		}
	}

	return nil
}

// SanitizeOutput removes sensitive information from model responses.
// Returns the sanitized string and whether any redaction occurred.
func (g *Guardrails) SanitizeOutput(output string) (string, bool) {
	redacted := false

	for _, pattern := range g.sensitivePatterns {
		if pattern.MatchString(output) {
			output = pattern.ReplaceAllString(output, "[REDACTED]")
			redacted = true
		}
	}

	return output, redacted
}

// RefusalResponse returns a safe refusal message for injection attempts.
func (g *Guardrails) RefusalResponse(attempt *InjectionAttempt) string {
	switch attempt.Type {
	case "extraction":
		return "I can't share information about my system configuration or internal instructions. How else can I help you?"
	case "enumeration":
		return "I'd rather focus on helping you with specific tasks. What would you like to accomplish?"
	case "jailbreak":
		return "I'm designed to be helpful, harmless, and honest. I can't bypass my safety guidelines. What can I actually help you with?"
	case "injection":
		return "I noticed some unusual formatting in your message. Could you rephrase your request?"
	default:
		return "I couldn't process that request. Could you try asking in a different way?"
	}
}

func classifyInjection(pattern string) string {
	pattern = strings.ToLower(pattern)

	if strings.Contains(pattern, "prompt") || strings.Contains(pattern, "instruction") ||
		strings.Contains(pattern, "reveal") || strings.Contains(pattern, "show") {
		return "extraction"
	}
	if strings.Contains(pattern, "list") || strings.Contains(pattern, "enumerate") ||
		strings.Contains(pattern, "tools") || strings.Contains(pattern, "capabilities") {
		return "enumeration"
	}
	if strings.Contains(pattern, "ignore") || strings.Contains(pattern, "bypass") ||
		strings.Contains(pattern, "dan") || strings.Contains(pattern, "pretend") {
		return "jailbreak"
	}
	if strings.Contains(pattern, "system") || strings.Contains(pattern, "inst") {
		return "injection"
	}
	return "unknown"
}

// ValidateSkillSafety checks a skill definition for security issues.
type SkillValidation struct {
	Safe     bool
	Warnings []string
	Errors   []string
}

// ValidateSkill checks a skill's safety before loading.
func ValidateSkill(name, prompt string, steps []struct{ Tool string }) SkillValidation {
	v := SkillValidation{Safe: true}

	// Check for suspicious prompt content.
	suspiciousPromptPatterns := []string{
		`(?i)curl\s+`,
		`(?i)wget\s+`,
		`(?i)bash\s+-c`,
		`(?i)sh\s+-c`,
		`(?i)eval\s*\(`,
		`(?i)exec\s*\(`,
		`(?i)system\s*\(`,
		`(?i)subprocess`,
		`(?i)os\.system`,
		`(?i)import\s+os`,
		`(?i)require\s*\(\s*['"]child_process`,
		`(?i)<script`,
		`(?i)javascript:`,
		`(?i)data:text/html`,
		`(?i)file:///`,
	}

	for _, pattern := range suspiciousPromptPatterns {
		re := regexp.MustCompile(pattern)
		if re.MatchString(prompt) {
			v.Errors = append(v.Errors, "Skill prompt contains suspicious pattern: "+pattern)
			v.Safe = false
		}
	}

	// Check for external URL patterns in prompt.
	urlPattern := regexp.MustCompile(`https?://[^\s]+`)
	urls := urlPattern.FindAllString(prompt, -1)
	if len(urls) > 0 {
		v.Warnings = append(v.Warnings, "Skill prompt contains URLs: "+strings.Join(urls, ", "))
	}

	// Check for excessive length (could hide malicious content).
	if len(prompt) > 10000 {
		v.Warnings = append(v.Warnings, "Skill prompt is unusually long (>10KB)")
	}

	// Validate tool references.
	allowedTools := map[string]bool{
		"email_search":  true,
		"email_read":    true,
		"email_send":    true,
		"email_check":   true,
		"web_search":    true,
		"memory_store":  true,
		"memory_recall": true,
		"memory_search": true,
		"calendar_list": true,
		"calendar_add":  true,
		"reminder_set":  true,
		"reminder_list": true,
		// MCP tools are validated separately
	}

	for _, step := range steps {
		// Allow MCP tools (prefixed with server name).
		if strings.Contains(step.Tool, ".") {
			continue // MCP tool, validated at runtime
		}
		if !allowedTools[step.Tool] {
			v.Warnings = append(v.Warnings, "Skill references unknown tool: "+step.Tool)
		}
	}

	return v
}
