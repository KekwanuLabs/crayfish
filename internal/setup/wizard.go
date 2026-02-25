// Package setup implements the web-based setup wizard for first-time Crayfish configuration.
// When Crayfish starts without an API key, it serves a friendly local web UI
// at the configured listen address (default :8119) so users can configure
// everything from their phone or laptop on the same network.
package setup

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

//go:embed templates/*.html
var templateFS embed.FS

// WizardConfig holds the setup wizard configuration.
type WizardConfig struct {
	ListenAddr string // Address to serve on (e.g., ":8119").
	ConfigPath string // Path to write the config file (e.g., "/etc/crayfish/crayfish.yaml").
	EnvPath    string // Path to write the env file (e.g., "/etc/crayfish/env").
	Version    string // Current Crayfish version.
}

// SetupData holds the user's input from the setup form.
type SetupData struct {
	// Identity — the Crayfish's given name and personality.
	Name        string `json:"name" yaml:"name"`
	Personality string `json:"personality,omitempty" yaml:"personality,omitempty"`

	Provider      string `json:"provider" yaml:"provider"`
	APIKey        string `json:"api_key" yaml:"api_key"`
	Endpoint      string `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	Model         string `json:"model,omitempty" yaml:"model,omitempty"`
	TelegramToken string `json:"telegram_token,omitempty" yaml:"telegram_token,omitempty"`
	BraveAPIKey   string `json:"brave_api_key,omitempty" yaml:"brave_api_key,omitempty"`
	AutoUpdate       bool   `json:"auto_update" yaml:"auto_update"`
}

// HardwareInfo contains detected device capabilities.
type HardwareInfo struct {
	TotalRAMGB     float64  `json:"total_ram_gb"`
	AvailableRAMGB float64  `json:"available_ram_gb"`
	CPUArch        string   `json:"cpu_arch"`
	Is64Bit        bool     `json:"is_64bit"`
	CanRunOllama   bool     `json:"can_run_ollama"`
	RecommendedModels []ModelRecommendation `json:"recommended_models"`
	Message        string   `json:"message"`
}

// ModelRecommendation suggests a model based on hardware.
type ModelRecommendation struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	RAMRequired float64 `json:"ram_required_gb"`
	Fits        bool   `json:"fits"`
	Recommended bool   `json:"recommended"`
}

// Wizard serves the setup web UI.
type Wizard struct {
	config WizardConfig
	logger *slog.Logger
	server *http.Server
	tmpl   *template.Template
	doneCh chan SetupData // Signals completion with the user's config.
}

// NewWizard creates a new setup wizard.
func NewWizard(cfg WizardConfig, logger *slog.Logger) *Wizard {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8119"
	}
	if cfg.ConfigPath == "" {
		cfg.ConfigPath = "crayfish.yaml"
	}
	if cfg.EnvPath == "" {
		cfg.EnvPath = "/etc/crayfish/env"
	}

	tmpl := template.Must(template.ParseFS(templateFS, "templates/*.html"))

	return &Wizard{
		config: cfg,
		logger: logger,
		tmpl:   tmpl,
		doneCh: make(chan SetupData, 1),
	}
}

// Start begins serving the setup wizard. It blocks until the user completes
// setup or the context is cancelled. Returns the setup data on success.
func (w *Wizard) Start(ctx context.Context) (*SetupData, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", w.handleIndex)
	mux.HandleFunc("/api/setup", w.handleSetup)
	mux.HandleFunc("/api/test-provider", w.handleTestProvider)
	mux.HandleFunc("/api/hardware", w.handleHardwareDetection)
	mux.HandleFunc("/api/ollama/status", w.handleOllamaStatus)
	mux.HandleFunc("/api/ollama/install", w.handleOllamaInstall)
	mux.HandleFunc("/api/ollama/pull", w.handleOllamaPull)
	mux.HandleFunc("/api/ollama/models", w.handleOllamaModels)
	mux.HandleFunc("/api/voice/status", w.handleVoiceStatus)
	mux.HandleFunc("/api/voice/install", w.handleVoiceInstall)
	mux.HandleFunc("/success", w.handleSuccess)

	w.server = &http.Server{
		Addr:         w.config.ListenAddr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	// Print local network addresses so users know where to connect.
	w.printAccessURLs()

	// Start serving in background.
	errCh := make(chan error, 1)
	go func() {
		if err := w.server.ListenAndServe(); err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	// Wait for either: setup completion, context cancellation, or server error.
	select {
	case data := <-w.doneCh:
		w.logger.Info("setup completed")
		w.server.Shutdown(context.Background())
		return &data, nil
	case err := <-errCh:
		return nil, fmt.Errorf("setup server: %w", err)
	case <-ctx.Done():
		w.server.Shutdown(context.Background())
		return nil, ctx.Err()
	}
}

// handleIndex serves the main setup page.
func (w *Wizard) handleIndex(rw http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(rw, r)
		return
	}

	data := map[string]interface{}{
		"Version": w.config.Version,
	}

	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := w.tmpl.ExecuteTemplate(rw, "index.html", data); err != nil {
		w.logger.Error("template error", "error", err)
		http.Error(rw, "Internal error", 500)
	}
}

// handleSetup processes the setup form submission.
func (w *Wizard) handleSetup(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var data SetupData
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		w.jsonError(rw, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate required fields.
	if data.Name == "" {
		data.Name = "Crayfish" // Default name if not provided
	}
	if data.Provider == "" {
		data.Provider = "anthropic"
	}
	// API key is only required for cloud providers, not for Ollama.
	if data.APIKey == "" && data.Provider != "ollama" {
		w.jsonError(rw, "API key is required for cloud providers", http.StatusBadRequest)
		return
	}

	// Write config file.
	if err := w.writeConfig(data); err != nil {
		w.logger.Error("failed to write config", "error", err)
		w.jsonError(rw, fmt.Sprintf("Failed to save config: %v", err), http.StatusInternalServerError)
		return
	}

	// Write env file (for systemd).
	if err := w.writeEnvFile(data); err != nil {
		w.logger.Warn("failed to write env file (non-fatal)", "error", err)
		// Non-fatal — the YAML config is the primary source.
	}

	w.logger.Info("configuration saved",
		"name", data.Name,
		"provider", data.Provider,
		"telegram", data.TelegramToken != "",
	)

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(map[string]string{"status": "ok"})

	// Signal completion.
	w.doneCh <- data
}

// handleTestProvider tests connectivity to the chosen LLM provider.
func (w *Wizard) handleTestProvider(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(rw, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Provider string `json:"provider"`
		APIKey   string `json:"api_key"`
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.jsonError(rw, "Invalid request", http.StatusBadRequest)
		return
	}

	// Quick connectivity check — just verify the API endpoint responds.
	endpoint := req.Endpoint
	if endpoint == "" {
		switch req.Provider {
		case "anthropic":
			endpoint = "https://api.anthropic.com/v1/messages"
		case "openai":
			endpoint = "https://api.openai.com/v1/models"
		case "grok":
			endpoint = "https://api.x.ai/v1/models"
		case "gemini":
			endpoint = "https://generativelanguage.googleapis.com/v1beta/models"
		case "ollama":
			endpoint = "http://localhost:11434/api/version"
		default:
			// For custom providers, just check if endpoint is reachable.
			if endpoint == "" {
				endpoint = "http://localhost:11434/api/version"
			}
		}
	}

	client := &http.Client{Timeout: 10 * time.Second}
	testReq, _ := http.NewRequest("GET", endpoint, nil)
	if req.APIKey != "" {
		switch req.Provider {
		case "anthropic":
			testReq.Header.Set("x-api-key", req.APIKey)
			testReq.Header.Set("anthropic-version", "2023-06-01")
		default:
			testReq.Header.Set("Authorization", "Bearer "+req.APIKey)
		}
	}

	resp, err := client.Do(testReq)
	if err != nil {
		w.jsonError(rw, fmt.Sprintf("Connection failed: %v", err), http.StatusOK)
		return
	}
	defer resp.Body.Close()

	// Any non-5xx response means the endpoint is reachable.
	if resp.StatusCode >= 500 {
		w.jsonError(rw, fmt.Sprintf("Server error: HTTP %d", resp.StatusCode), http.StatusOK)
		return
	}

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(map[string]string{"status": "ok", "message": "Connected successfully"})
}

// handleHardwareDetection returns info about the device to help choose the right model.
func (w *Wizard) handleHardwareDetection(rw http.ResponseWriter, r *http.Request) {
	info := HardwareInfo{
		CPUArch: goruntime.GOARCH,
		Is64Bit: goruntime.GOARCH == "amd64" || goruntime.GOARCH == "arm64",
	}

	// Get memory info by reading /proc/meminfo on Linux or using sysctl on macOS.
	info.TotalRAMGB, info.AvailableRAMGB = getMemoryInfo()

	// Ollama requires 64-bit ARM or x86.
	info.CanRunOllama = info.Is64Bit && (goruntime.GOARCH == "amd64" || goruntime.GOARCH == "arm64")

	// Build model recommendations based on available RAM.
	models := []ModelRecommendation{
		{ID: "tinyllama", Name: "TinyLlama", Description: "Smallest and fastest, good for simple tasks", RAMRequired: 1.0},
		{ID: "gemma:2b", Name: "Gemma 2B", Description: "Lightweight and capable", RAMRequired: 2.0},
		{ID: "phi3", Name: "Phi-3", Description: "Best balance of speed and smarts", RAMRequired: 4.0},
		{ID: "llama3.2:3b", Name: "Llama 3.2", Description: "Very capable, great for conversations", RAMRequired: 4.0},
		{ID: "mistral", Name: "Mistral", Description: "Most powerful, needs more memory", RAMRequired: 8.0},
	}

	var recommended *ModelRecommendation
	for i := range models {
		models[i].Fits = models[i].RAMRequired <= info.AvailableRAMGB
		if models[i].Fits && recommended == nil {
			// Recommend the largest model that fits.
		}
		// Find the best fitting model (largest that fits).
		if models[i].Fits {
			recommended = &models[i]
		}
	}
	if recommended != nil {
		recommended.Recommended = true
	}
	info.RecommendedModels = models

	// Generate friendly message.
	if !info.CanRunOllama {
		info.Message = "Your device uses a 32-bit processor, which can't run on-device AI. But don't worry — you can still use Cloud AI!"
	} else if info.AvailableRAMGB < 1.0 {
		info.Message = "Your device doesn't have enough memory for on-device AI. We recommend using Cloud AI instead."
	} else if info.AvailableRAMGB < 2.0 {
		info.Message = fmt.Sprintf("Your device has about %.0f GB of memory. TinyLlama will work, but responses may be basic.", info.AvailableRAMGB)
	} else if info.AvailableRAMGB < 4.0 {
		info.Message = fmt.Sprintf("Your device has about %.0f GB of memory. Gemma 2B is a great choice!", info.AvailableRAMGB)
	} else if info.AvailableRAMGB < 8.0 {
		info.Message = fmt.Sprintf("Your device has about %.0f GB of memory. Phi-3 will give you excellent results!", info.AvailableRAMGB)
	} else {
		info.Message = fmt.Sprintf("Your device has %.0f GB of memory — plenty of room! You can use any model.", info.AvailableRAMGB)
	}

	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(info)
}

// getMemoryInfo returns total and available RAM in GB.
func getMemoryInfo() (total, available float64) {
	// Try to read from /proc/meminfo (Linux).
	data, err := os.ReadFile("/proc/meminfo")
	if err == nil {
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "MemTotal:") {
				var kb int64
				fmt.Sscanf(line, "MemTotal: %d kB", &kb)
				total = float64(kb) / 1024 / 1024
			}
			if strings.HasPrefix(line, "MemAvailable:") {
				var kb int64
				fmt.Sscanf(line, "MemAvailable: %d kB", &kb)
				available = float64(kb) / 1024 / 1024
			}
		}
		return total, available
	}

	// Fallback: estimate based on runtime stats (less accurate but works everywhere).
	var m goruntime.MemStats
	goruntime.ReadMemStats(&m)

	// This is a rough estimate — we can't easily get total system RAM from Go.
	// Default to 4GB if we can't detect.
	return 4.0, 4.0
}

// handleSuccess shows the "all done" page.
func (w *Wizard) handleSuccess(rw http.ResponseWriter, r *http.Request) {
	data := map[string]interface{}{
		"Version": w.config.Version,
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := w.tmpl.ExecuteTemplate(rw, "success.html", data); err != nil {
		w.logger.Error("template error", "error", err)
	}
}

func (w *Wizard) writeConfig(data SetupData) error {
	// Ensure config directory exists.
	dir := filepath.Dir(w.config.ConfigPath)
	if dir != "." {
		os.MkdirAll(dir, 0755)
	}

	yamlData, err := yaml.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	header := "# Crayfish configuration — generated by setup wizard.\n# Edit this file or use environment variables to override.\n\n"
	return os.WriteFile(w.config.ConfigPath, []byte(header+string(yamlData)), 0600)
}

func (w *Wizard) writeEnvFile(data SetupData) error {
	dir := filepath.Dir(w.config.EnvPath)
	if dir != "." {
		os.MkdirAll(dir, 0755)
	}

	var lines []string
	lines = append(lines, "# Crayfish environment — generated by setup wizard.")
	if data.Name != "" {
		lines = append(lines, fmt.Sprintf("CRAYFISH_NAME=%s", data.Name))
	}
	lines = append(lines, fmt.Sprintf("CRAYFISH_API_KEY=%s", data.APIKey))
	lines = append(lines, fmt.Sprintf("CRAYFISH_PROVIDER=%s", data.Provider))

	if data.Model != "" {
		lines = append(lines, fmt.Sprintf("CRAYFISH_MODEL=%s", data.Model))
	}
	if data.Endpoint != "" {
		lines = append(lines, fmt.Sprintf("CRAYFISH_ENDPOINT=%s", data.Endpoint))
	}
	if data.TelegramToken != "" {
		lines = append(lines, fmt.Sprintf("CRAYFISH_TELEGRAM_TOKEN=%s", data.TelegramToken))
	}
	if data.BraveAPIKey != "" {
		lines = append(lines, fmt.Sprintf("CRAYFISH_BRAVE_API_KEY=%s", data.BraveAPIKey))
	}
	if data.AutoUpdate {
		lines = append(lines, "CRAYFISH_AUTO_UPDATE=true")
	}

	return os.WriteFile(w.config.EnvPath, []byte(strings.Join(lines, "\n")+"\n"), 0600)
}

func (w *Wizard) jsonError(rw http.ResponseWriter, msg string, code int) {
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(code)
	json.NewEncoder(rw).Encode(map[string]string{"error": msg})
}

func (w *Wizard) printAccessURLs() {
	port := w.config.ListenAddr
	if strings.HasPrefix(port, ":") {
		port = port[1:]
	}

	fmt.Println()
	fmt.Println("  Crayfish Setup Wizard")
	fmt.Println("  =====================")
	fmt.Println()
	fmt.Printf("  Open your browser to set up Crayfish:\n\n")

	// Get local IP addresses.
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
				fmt.Printf("    http://%s:%s\n", ipnet.IP.String(), port)
			}
		}
	}
	fmt.Printf("    http://localhost:%s\n", port)
	fmt.Println()
	fmt.Println("  Waiting for setup to complete...")
	fmt.Println()
}

// OllamaStatus represents the current state of Ollama on this device.
type OllamaStatus struct {
	Installed       bool     `json:"installed"`
	Running         bool     `json:"running"`
	Version         string   `json:"version,omitempty"`
	InstalledModels []string `json:"installed_models,omitempty"`
	Message         string   `json:"message"`
}

// handleOllamaStatus checks if Ollama is installed and running.
func (w *Wizard) handleOllamaStatus(rw http.ResponseWriter, r *http.Request) {
	status := OllamaStatus{}

	// Check if ollama binary exists.
	ollamaPath, err := exec.LookPath("ollama")
	status.Installed = err == nil && ollamaPath != ""

	if !status.Installed {
		status.Message = "Ollama is not installed. We'll help you set it up!"
		w.writeJSON(rw, status)
		return
	}

	// Check if Ollama is running by hitting its API.
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://localhost:11434/api/version")
	if err != nil {
		status.Message = "Ollama is installed but not running. Starting it..."
		// Try to start Ollama.
		go w.startOllama()
		w.writeJSON(rw, status)
		return
	}
	defer resp.Body.Close()

	status.Running = true

	// Get version.
	var versionResp struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&versionResp); err == nil {
		status.Version = versionResp.Version
	}

	// Get list of installed models.
	modelsResp, err := client.Get("http://localhost:11434/api/tags")
	if err == nil {
		defer modelsResp.Body.Close()
		var tags struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := json.NewDecoder(modelsResp.Body).Decode(&tags); err == nil {
			for _, m := range tags.Models {
				status.InstalledModels = append(status.InstalledModels, m.Name)
			}
		}
	}

	if len(status.InstalledModels) > 0 {
		status.Message = fmt.Sprintf("Ollama is ready with %d model(s) installed.", len(status.InstalledModels))
	} else {
		status.Message = "Ollama is running. Let's download a model for your device!"
	}

	w.writeJSON(rw, status)
}

// startOllama attempts to start the Ollama service.
func (w *Wizard) startOllama() {
	cmd := exec.Command("ollama", "serve")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		w.logger.Error("failed to start ollama", "error", err)
	}
}

// handleOllamaInstall downloads and installs Ollama.
func (w *Wizard) handleOllamaInstall(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.jsonError(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Set headers for streaming response.
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-cache")
	rw.Header().Set("Connection", "keep-alive")
	flusher, ok := rw.(http.Flusher)
	if !ok {
		w.jsonError(rw, "streaming not supported", http.StatusInternalServerError)
		return
	}

	sendEvent := func(event, data string) {
		fmt.Fprintf(rw, "event: %s\ndata: %s\n\n", event, data)
		flusher.Flush()
	}

	sendEvent("progress", `{"status": "checking", "message": "Checking your system..."}`)

	// Determine install method based on OS.
	switch goruntime.GOOS {
	case "linux":
		w.installOllamaLinux(sendEvent)

	case "darwin":
		w.installOllamaMacOS(sendEvent)

	case "windows":
		w.installOllamaWindows(sendEvent)

	default:
		sendEvent("manual", `{"os": "`+goruntime.GOOS+`", "message": "We don't have automatic installation for your system yet.", "url": "https://ollama.com/download", "instructions": "Please download and install Ollama from ollama.com, then click 'I've installed it' to continue."}`)
		return
	}

	// Verify installation (for successful auto-installs).
	w.verifyOllamaInstall(sendEvent)
}

// installOllamaLinux handles Linux installation via official script.
func (w *Wizard) installOllamaLinux(sendEvent func(string, string)) {
	sendEvent("progress", `{"status": "installing", "message": "Installing Ollama (this may take a minute)..."}`)

	cmd := exec.Command("sh", "-c", "curl -fsSL https://ollama.com/install.sh | sh")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Check if it's a permission issue
		outputStr := string(output)
		if strings.Contains(outputStr, "permission denied") || strings.Contains(outputStr, "sudo") {
			sendEvent("manual", `{"os": "linux", "message": "Installation requires administrator access.", "command": "curl -fsSL https://ollama.com/install.sh | sh", "instructions": "Please run this command in your terminal, then click 'I've installed it' to continue."}`)
		} else {
			sendEvent("error", fmt.Sprintf(`{"message": "Installation failed: %s"}`, err))
		}
		return
	}

	sendEvent("progress", `{"status": "starting", "message": "Starting Ollama service..."}`)
	go w.startOllama()
	time.Sleep(2 * time.Second)
}

// installOllamaMacOS handles macOS installation via Homebrew or manual download.
func (w *Wizard) installOllamaMacOS(sendEvent func(string, string)) {
	// Check if Homebrew is installed.
	_, err := exec.LookPath("brew")
	if err == nil {
		// Homebrew available - try to install via brew.
		sendEvent("progress", `{"status": "installing", "message": "Installing Ollama via Homebrew..."}`)

		cmd := exec.Command("brew", "install", "ollama")
		output, err := cmd.CombinedOutput()
		if err != nil {
			w.logger.Warn("brew install failed", "error", err, "output", string(output))
			// Fall back to manual installation.
			sendEvent("manual", `{"os": "macos", "message": "Homebrew installation didn't work. Let's try the app instead.", "url": "https://ollama.com/download/Ollama-darwin.zip", "instructions": "Download the Ollama app, open the zip file, and drag Ollama to your Applications folder. Then open it and click 'I've installed it' to continue."}`)
			return
		}

		sendEvent("progress", `{"status": "starting", "message": "Starting Ollama..."}`)
		go w.startOllama()
		time.Sleep(2 * time.Second)
		return
	}

	// No Homebrew - guide to manual download.
	sendEvent("manual", `{"os": "macos", "message": "Let's install the Ollama app.", "url": "https://ollama.com/download/Ollama-darwin.zip", "instructions": "Download the Ollama app, open the zip file, and drag Ollama to your Applications folder. Then open it and click 'I've installed it' to continue."}`)
}

// installOllamaWindows handles Windows installation via winget or manual download.
func (w *Wizard) installOllamaWindows(sendEvent func(string, string)) {
	// Check if winget is available.
	_, err := exec.LookPath("winget")
	if err == nil {
		// winget available - try to install.
		sendEvent("progress", `{"status": "installing", "message": "Installing Ollama via Windows Package Manager..."}`)

		cmd := exec.Command("winget", "install", "--id", "Ollama.Ollama", "-e", "--accept-source-agreements", "--accept-package-agreements")
		output, err := cmd.CombinedOutput()
		if err != nil {
			w.logger.Warn("winget install failed", "error", err, "output", string(output))
			// Fall back to manual installation.
			sendEvent("manual", `{"os": "windows", "message": "Automatic installation didn't work. Let's download it manually.", "url": "https://ollama.com/download/OllamaSetup.exe", "instructions": "Download and run the installer. Once it's done, click 'I've installed it' to continue."}`)
			return
		}

		sendEvent("progress", `{"status": "starting", "message": "Starting Ollama..."}`)
		// On Windows, Ollama typically starts automatically after install.
		time.Sleep(3 * time.Second)
		return
	}

	// No winget - guide to manual download.
	sendEvent("manual", `{"os": "windows", "message": "Let's download the Ollama installer.", "url": "https://ollama.com/download/OllamaSetup.exe", "instructions": "Download and run the installer. Once it's done, click 'I've installed it' to continue."}`)
}

// verifyOllamaInstall checks if Ollama is running after installation.
func (w *Wizard) verifyOllamaInstall(sendEvent func(string, string)) {
	client := &http.Client{Timeout: 5 * time.Second}
	var running bool
	for i := 0; i < 10; i++ {
		resp, err := client.Get("http://localhost:11434/api/version")
		if err == nil {
			resp.Body.Close()
			running = true
			break
		}
		time.Sleep(time.Second)
	}

	if running {
		sendEvent("complete", `{"installed": true, "running": true, "message": "Ollama installed successfully!"}`)
	} else {
		sendEvent("complete", `{"installed": true, "running": false, "message": "Ollama installed! You may need to start it manually."}`)
	}
}

// handleOllamaPull downloads a model with progress streaming.
func (w *Wizard) handleOllamaPull(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.jsonError(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model == "" {
		w.jsonError(rw, "model name required", http.StatusBadRequest)
		return
	}

	// Set headers for streaming response.
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-cache")
	rw.Header().Set("Connection", "keep-alive")
	flusher, ok := rw.(http.Flusher)
	if !ok {
		w.jsonError(rw, "streaming not supported", http.StatusInternalServerError)
		return
	}

	sendEvent := func(event, data string) {
		fmt.Fprintf(rw, "event: %s\ndata: %s\n\n", event, data)
		flusher.Flush()
	}

	w.logger.Info("pulling ollama model", "model", req.Model)
	sendEvent("progress", fmt.Sprintf(`{"status": "starting", "message": "Starting download of %s..."}`, req.Model))

	// Call Ollama API to pull the model.
	pullReq := struct {
		Name   string `json:"name"`
		Stream bool   `json:"stream"`
	}{
		Name:   req.Model,
		Stream: true,
	}
	pullBody, _ := json.Marshal(pullReq)

	client := &http.Client{Timeout: 30 * time.Minute} // Models can be large.
	resp, err := client.Post("http://localhost:11434/api/pull", "application/json", strings.NewReader(string(pullBody)))
	if err != nil {
		sendEvent("error", fmt.Sprintf(`{"message": "Failed to connect to Ollama: %s"}`, err))
		return
	}
	defer resp.Body.Close()

	// Stream the progress from Ollama.
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			sendEvent("error", fmt.Sprintf(`{"message": "Stream error: %s"}`, err))
			return
		}

		var progress struct {
			Status    string `json:"status"`
			Digest    string `json:"digest,omitempty"`
			Total     int64  `json:"total,omitempty"`
			Completed int64  `json:"completed,omitempty"`
		}
		if err := json.Unmarshal(line, &progress); err != nil {
			continue
		}

		// Calculate percentage if we have totals.
		percent := 0
		if progress.Total > 0 {
			percent = int(float64(progress.Completed) / float64(progress.Total) * 100)
		}

		// Send progress event.
		progressData := fmt.Sprintf(`{"status": "%s", "percent": %d, "completed": %d, "total": %d}`,
			progress.Status, percent, progress.Completed, progress.Total)
		sendEvent("progress", progressData)

		// Check if complete.
		if progress.Status == "success" {
			sendEvent("complete", fmt.Sprintf(`{"model": "%s", "message": "Model downloaded successfully!"}`, req.Model))
			return
		}
	}

	sendEvent("complete", fmt.Sprintf(`{"model": "%s", "message": "Download complete!"}`, req.Model))
}

// handleOllamaModels returns the list of locally installed models.
func (w *Wizard) handleOllamaModels(rw http.ResponseWriter, r *http.Request) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://localhost:11434/api/tags")
	if err != nil {
		w.jsonError(rw, "Ollama not running", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()

	var tags struct {
		Models []struct {
			Name       string `json:"name"`
			Size       int64  `json:"size"`
			ModifiedAt string `json:"modified_at"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		w.jsonError(rw, "failed to parse response", http.StatusInternalServerError)
		return
	}

	// Convert to simpler format.
	models := make([]map[string]interface{}, len(tags.Models))
	for i, m := range tags.Models {
		sizeGB := float64(m.Size) / 1024 / 1024 / 1024
		models[i] = map[string]interface{}{
			"name":    m.Name,
			"size_gb": fmt.Sprintf("%.1f", sizeGB),
		}
	}

	w.writeJSON(rw, map[string]interface{}{"models": models})
}

func (w *Wizard) writeJSON(rw http.ResponseWriter, data interface{}) {
	rw.Header().Set("Content-Type", "application/json")
	json.NewEncoder(rw).Encode(data)
}

// VoiceStatus contains info about voice recognition capabilities.
type VoiceStatus struct {
	Supported       bool   `json:"supported"`        // Device can run whisper
	Installed       bool   `json:"installed"`        // Whisper is installed
	RecommendedModel string `json:"recommended_model"` // Best model for this device
	DeviceRAMMB     int    `json:"device_ram_mb"`
	DeviceArch      string `json:"device_arch"`
	Message         string `json:"message"`
}

// handleVoiceStatus checks if voice recognition is available/supported.
func (w *Wizard) handleVoiceStatus(rw http.ResponseWriter, r *http.Request) {
	status := VoiceStatus{
		DeviceArch: goruntime.GOARCH,
	}

	// Get RAM info
	totalGB, _ := getMemoryInfo()
	status.DeviceRAMMB = int(totalGB * 1024)

	// Determine if device can run whisper
	if status.DeviceRAMMB < 600 {
		status.Supported = false
		status.Message = "Your device doesn't have enough memory for voice recognition. You can still use text input!"
		w.writeJSON(rw, status)
		return
	}

	status.Supported = true

	// Recommend model based on RAM
	if status.DeviceRAMMB < 1500 {
		status.RecommendedModel = "tiny"
	} else if status.DeviceRAMMB < 3000 {
		status.RecommendedModel = "base"
	} else if status.DeviceRAMMB < 6000 {
		status.RecommendedModel = "small"
	} else {
		status.RecommendedModel = "medium"
	}

	// Check if whisper is installed
	for _, cmd := range []string{"whisper", "whisper-cpp", "main"} {
		if _, err := exec.LookPath(cmd); err == nil {
			status.Installed = true
			break
		}
	}

	// Also check our install directory
	whisperPath := filepath.Join(os.Getenv("HOME"), ".crayfish", "whisper", "bin", "whisper")
	if _, err := os.Stat(whisperPath); err == nil {
		status.Installed = true
	}

	if status.Installed {
		status.Message = "Voice recognition is ready!"
	} else {
		status.Message = "Voice recognition will be set up automatically in the background."
	}

	w.writeJSON(rw, status)
}

// handleVoiceInstall streams voice recognition installation progress.
func (w *Wizard) handleVoiceInstall(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.jsonError(rw, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Set headers for streaming response
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.Header().Set("Cache-Control", "no-cache")
	rw.Header().Set("Connection", "keep-alive")
	flusher, ok := rw.(http.Flusher)
	if !ok {
		w.jsonError(rw, "streaming not supported", http.StatusInternalServerError)
		return
	}

	sendEvent := func(event, data string) {
		fmt.Fprintf(rw, "event: %s\ndata: %s\n\n", event, data)
		flusher.Flush()
	}

	// Check device capability
	totalGB, _ := getMemoryInfo()
	if totalGB < 0.6 {
		sendEvent("error", `{"message": "Device doesn't have enough memory for voice recognition"}`)
		return
	}

	sendEvent("progress", `{"status": "checking", "percent": 5, "message": "Checking system..."}`)

	// Determine model
	var model string
	ramMB := int(totalGB * 1024)
	if ramMB < 1500 {
		model = "tiny"
	} else if ramMB < 3000 {
		model = "base"
	} else {
		model = "small"
	}

	whisperDir := filepath.Join(os.Getenv("HOME"), ".crayfish", "whisper")
	binDir := filepath.Join(whisperDir, "bin")
	modelsDir := filepath.Join(whisperDir, "models")
	srcDir := filepath.Join(whisperDir, "src", "whisper.cpp")

	// Create directories
	os.MkdirAll(binDir, 0755)
	os.MkdirAll(modelsDir, 0755)

	// Check if whisper binary exists
	whisperBin := filepath.Join(binDir, "whisper")
	if _, err := os.Stat(whisperBin); err != nil {
		// Need to compile from source
		sendEvent("progress", `{"status": "downloading", "percent": 10, "message": "Downloading whisper.cpp source..."}`)

		// Clone repository
		if _, err := os.Stat(filepath.Join(srcDir, "Makefile")); err != nil {
			cmd := exec.Command("git", "clone", "--depth", "1", "https://github.com/ggml-org/whisper.cpp.git", srcDir)
			if output, err := cmd.CombinedOutput(); err != nil {
				sendEvent("error", fmt.Sprintf(`{"message": "Failed to clone whisper.cpp: %s"}`, string(output)))
				return
			}
		}

		sendEvent("progress", `{"status": "compiling", "percent": 20, "message": "Compiling whisper.cpp (this takes a few minutes on Pi)..."}`)

		// Compile
		cores := goruntime.NumCPU()
		if cores > 2 {
			cores = 2 // Don't overwhelm small devices
		}
		cmd := exec.Command("make", fmt.Sprintf("-j%d", cores))
		cmd.Dir = srcDir
		if output, err := cmd.CombinedOutput(); err != nil {
			sendEvent("error", fmt.Sprintf(`{"message": "Compilation failed: %s"}`, string(output)))
			return
		}

		// Copy binary
		sendEvent("progress", `{"status": "installing", "percent": 50, "message": "Installing binary..."}`)
		srcBin := filepath.Join(srcDir, "main")
		if err := copyFileSimple(srcBin, whisperBin); err != nil {
			sendEvent("error", fmt.Sprintf(`{"message": "Failed to install binary: %s"}`, err))
			return
		}
		os.Chmod(whisperBin, 0755)
	}

	// Download model if needed
	modelPath := filepath.Join(modelsDir, fmt.Sprintf("ggml-%s.bin", model))
	if _, err := os.Stat(modelPath); err != nil {
		sendEvent("progress", fmt.Sprintf(`{"status": "downloading_model", "percent": 60, "message": "Downloading %s voice model..."}`, model))

		modelURL := fmt.Sprintf("https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-%s.bin", model)

		// Download model
		client := &http.Client{Timeout: 30 * time.Minute}
		resp, err := client.Get(modelURL)
		if err != nil {
			sendEvent("error", fmt.Sprintf(`{"message": "Failed to download model: %s"}`, err))
			return
		}
		defer resp.Body.Close()

		tmpPath := modelPath + ".tmp"
		f, err := os.Create(tmpPath)
		if err != nil {
			sendEvent("error", fmt.Sprintf(`{"message": "Failed to create model file: %s"}`, err))
			return
		}

		totalSize := resp.ContentLength
		var downloaded int64
		buf := make([]byte, 32*1024)

		for {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				f.Write(buf[:n])
				downloaded += int64(n)

				if totalSize > 0 {
					pct := int(float64(downloaded) / float64(totalSize) * 30) + 60 // 60-90%
					sizeMB := downloaded / 1024 / 1024
					totalMB := totalSize / 1024 / 1024
					sendEvent("progress", fmt.Sprintf(`{"status": "downloading_model", "percent": %d, "message": "Downloading model... %dMB / %dMB"}`, pct, sizeMB, totalMB))
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				f.Close()
				os.Remove(tmpPath)
				sendEvent("error", fmt.Sprintf(`{"message": "Download failed: %s"}`, readErr))
				return
			}
		}
		f.Close()
		os.Rename(tmpPath, modelPath)
	}

	sendEvent("progress", `{"status": "verifying", "percent": 95, "message": "Verifying installation..."}`)

	// Quick verification - just check files exist
	if _, err := os.Stat(whisperBin); err != nil {
		sendEvent("error", `{"message": "Whisper binary not found after installation"}`)
		return
	}
	if _, err := os.Stat(modelPath); err != nil {
		sendEvent("error", `{"message": "Model file not found after download"}`)
		return
	}

	sendEvent("complete", fmt.Sprintf(`{"message": "Voice recognition ready!", "model": "%s"}`, model))
}

func copyFileSimple(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
