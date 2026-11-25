package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/amiraq1/nabd/internal/llm"
	"golang.org/x/term"
)

const maxKeyFileSize = 4096

// SavedConfig stores non-secret provider and model settings.
// API keys are strictly stored in separate, permissions-restricted files.
type SavedConfig struct {
	Provider string `json:"provider,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
	Model    string `json:"model,omitempty"`
}

// defaultConfigDir returns the standard configuration directory for NabdCoder.
func defaultConfigDir() string {
	configDir, err := os.UserConfigDir()
	if err != nil || configDir == "" {
		home := os.Getenv("HOME")
		if home == "" {
			home = os.Getenv("USERPROFILE")
		}
		if home != "" {
			return filepath.Join(home, ".config", "nabd")
		}
		return ".nabd"
	}
	return filepath.Join(configDir, "nabd")
}

// validateConfigDirNotInWorkspace verifies that the configuration directory
// is not located inside the current project workspace or a .git repository.
func validateConfigDirNotInWorkspace(configDir string, workspaceDir string) error {
	if configDir == "" {
		return errors.New("config directory cannot be empty")
	}

	absConfig, err := filepath.Abs(filepath.Clean(configDir))
	if err != nil {
		return fmt.Errorf("resolving config directory: %w", err)
	}

	// Reject paths containing a .git component
	parts := strings.Split(absConfig, string(filepath.Separator))
	for _, part := range parts {
		if part == ".git" {
			return fmt.Errorf("insecure config location: config directory cannot reside inside a .git folder: %s", configDir)
		}
	}

	if workspaceDir == "" {
		return nil
	}

	absWorkspace, err := filepath.Abs(filepath.Clean(workspaceDir))
	if err != nil {
		return fmt.Errorf("resolving workspace directory: %w", err)
	}

	if realConfig, err := filepath.EvalSymlinks(absConfig); err == nil {
		absConfig = realConfig
	}
	if realWorkspace, err := filepath.EvalSymlinks(absWorkspace); err == nil {
		absWorkspace = realWorkspace
	}

	c1 := absConfig
	w1 := absWorkspace
	if runtime.GOOS == "windows" {
		c1 = strings.ToLower(c1)
		w1 = strings.ToLower(w1)
	}

	if c1 == w1 {
		return fmt.Errorf("insecure config location: config directory %q cannot reside inside workspace %q", configDir, workspaceDir)
	}

	rel, err := filepath.Rel(w1, c1)
	if err == nil && !strings.HasPrefix(rel, "..") && rel != "." {
		return fmt.Errorf("insecure config location: config directory %q cannot reside inside workspace %q", configDir, workspaceDir)
	}

	return nil
}

// readSavedKey reads and validates an API key file with strict permission and size checks.
func readSavedKey(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("insecure key file: symlinks are not allowed: %s", path)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("insecure key file: not a regular file: %s", path)
	}
	if info.Size() > maxKeyFileSize {
		return "", fmt.Errorf("insecure key file: file size %d bytes exceeds %d bytes limit: %s", info.Size(), maxKeyFileSize, path)
	}

	if runtime.GOOS != "windows" {
		perm := info.Mode().Perm()
		if perm&0077 != 0 {
			return "", fmt.Errorf("insecure key file permissions %04o for %s; permissions must be 0600 or stricter", perm, path)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// writeSecureFile writes data to a temporary file, sets permissions, syncs to disk,
// and atomically renames it to filename in dir.
func writeSecureFile(dir, filename string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating directory %s: %w", dir, err)
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(dir, 0700)
	}

	targetPath := filepath.Join(dir, filename)
	tmpFile, err := os.CreateTemp(dir, filename+".tmp-*")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	tmpPath := tmpFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmpFile.Chmod(perm); err != nil {
		return fmt.Errorf("setting permissions on %s: %w", tmpPath, err)
	}

	if _, err := tmpFile.Write(data); err != nil {
		return fmt.Errorf("writing to %s: %w", tmpPath, err)
	}

	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", tmpPath, err)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmpPath, err)
	}

	if runtime.GOOS == "windows" {
		_ = os.Remove(targetPath)
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", tmpPath, targetPath, err)
	}

	cleanup = false
	return nil
}

// keyFilenameForProvider returns the designated secret filename for a provider.
func keyFilenameForProvider(provider string) string {
	if strings.ToLower(provider) == llm.ProviderGemini {
		return "gemini_api_key"
	}
	return "api_key"
}

// SaveConfigAndKey persists the non-secret configuration and API key into configDir.
// The key file is written first with 0600 permissions; config.json is written only
// after the key is successfully persisted.
func SaveConfigAndKey(configDir string, cfg SavedConfig, apiKey string) error {
	keyFilename := keyFilenameForProvider(cfg.Provider)

	if apiKey != "" {
		if err := writeSecureFile(configDir, keyFilename, []byte(apiKey+"\n"), 0600); err != nil {
			return fmt.Errorf("saving API key: %w", err)
		}
	}

	configBytes, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling configuration: %w", err)
	}
	configBytes = append(configBytes, '\n')

	if err := writeSecureFile(configDir, "config.json", configBytes, 0600); err != nil {
		return fmt.Errorf("saving config.json: %w", err)
	}

	return nil
}

// ResetConfig removes only the saved configuration and key files.
func ResetConfig(configDir string) error {
	files := []string{"config.json", "api_key", "gemini_api_key"}
	var errs []error
	for _, f := range files {
		p := filepath.Join(configDir, f)
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func loadSavedConfig(configDir string) (SavedConfig, error) {
	var cfg SavedConfig
	path := filepath.Join(configDir, "config.json")
	info, err := os.Lstat(path)
	if err != nil {
		return cfg, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return cfg, fmt.Errorf("insecure config file: symlinks are not allowed: %s", path)
	}
	if !info.Mode().IsRegular() {
		return cfg, fmt.Errorf("insecure config file: not a regular file: %s", path)
	}
	if info.Size() > 65536 {
		return cfg, fmt.Errorf("config file too large (%d bytes)", info.Size())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parsing %s: %w", path, err)
	}
	return cfg, nil
}

// LoadEffectiveConfig resolves configuration from Environment > Saved Config > Defaults.
// It enforces strict provider isolation: an OpenAI key will never be sent to Gemini or vice-versa.
// Returned keySource is one of: "env", "env_file", "saved", or "none".
func LoadEffectiveConfig(configDir string) (llm.Config, string, error) {
	savedCfg, _ := loadSavedConfig(configDir)

	provider := strings.ToLower(strings.TrimSpace(os.Getenv("NABD_PROVIDER")))
	if provider == "" {
		if savedCfg.Provider != "" {
			provider = strings.ToLower(strings.TrimSpace(savedCfg.Provider))
		} else {
			provider = llm.ProviderOpenAI
		}
	}

	baseURL := strings.TrimSpace(os.Getenv("NABD_BASE_URL"))
	if baseURL == "" {
		if savedCfg.BaseURL != "" {
			baseURL = savedCfg.BaseURL
		} else {
			if provider == llm.ProviderGemini {
				baseURL = llm.DefaultGeminiBaseURL
			} else {
				baseURL = llm.DefaultBaseURL
			}
		}
	}

	model := strings.TrimSpace(os.Getenv("NABD_MODEL"))
	if model == "" {
		if savedCfg.Model != "" {
			model = savedCfg.Model
		} else {
			if provider == llm.ProviderGemini {
				model = llm.DefaultGeminiModel
			} else {
				model = llm.DefaultModel
			}
		}
	}

	var apiKey string
	keySource := "none"

	if provider == llm.ProviderGemini {
		geminiKeyVal := strings.TrimSpace(os.Getenv("NABD_GEMINI_API_KEY"))
		geminiKeyFile := strings.TrimSpace(os.Getenv("NABD_GEMINI_API_KEY_FILE"))
		if geminiKeyVal != "" && geminiKeyFile != "" {
			return llm.Config{}, "", errors.New("both NABD_GEMINI_API_KEY and NABD_GEMINI_API_KEY_FILE are set; specify only one")
		}
		if geminiKeyVal != "" {
			apiKey = geminiKeyVal
			keySource = "env"
		} else if geminiKeyFile != "" {
			k, err := readSavedKey(geminiKeyFile)
			if err != nil {
				return llm.Config{}, "", fmt.Errorf("reading NABD_GEMINI_API_KEY_FILE: %w", err)
			}
			apiKey = k
			keySource = "env_file"
		} else {
			savedKeyPath := filepath.Join(configDir, "gemini_api_key")
			if _, err := os.Lstat(savedKeyPath); err == nil {
				k, err := readSavedKey(savedKeyPath)
				if err != nil {
					return llm.Config{}, "", fmt.Errorf("reading saved gemini_api_key: %w", err)
				}
				if k != "" {
					apiKey = k
					keySource = "saved"
				}
			}
		}
	} else {
		apiVal := strings.TrimSpace(os.Getenv("NABD_API_KEY"))
		apiFile := strings.TrimSpace(os.Getenv("NABD_API_KEY_FILE"))
		if apiVal != "" && apiFile != "" {
			return llm.Config{}, "", errors.New("both NABD_API_KEY and NABD_API_KEY_FILE are set; specify only one")
		}
		if apiVal != "" {
			apiKey = apiVal
			keySource = "env"
		} else if apiFile != "" {
			k, err := readSavedKey(apiFile)
			if err != nil {
				return llm.Config{}, "", fmt.Errorf("reading NABD_API_KEY_FILE: %w", err)
			}
			apiKey = k
			keySource = "env_file"
		} else {
			savedKeyPath := filepath.Join(configDir, "api_key")
			if _, err := os.Lstat(savedKeyPath); err == nil {
				k, err := readSavedKey(savedKeyPath)
				if err != nil {
					return llm.Config{}, "", fmt.Errorf("reading saved api_key: %w", err)
				}
				if k != "" {
					apiKey = k
					keySource = "saved"
				}
			}
		}
	}

	var errs []error
	var geminiSearch bool
	if v := strings.TrimSpace(os.Getenv("NABD_GEMINI_SEARCH")); v != "" {
		if v == "1" {
			geminiSearch = true
		} else if v == "0" {
			geminiSearch = false
		} else {
			errs = append(errs, fmt.Errorf("invalid NABD_GEMINI_SEARCH %q: must be 1, 0, or empty", v))
		}
	}

	timeout := llm.DefaultTimeout
	if v := strings.TrimSpace(os.Getenv("NABD_TIMEOUT")); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 5*time.Second || d > 600*time.Second {
			errs = append(errs, fmt.Errorf("invalid NABD_TIMEOUT %q: allowed range 5s-600s", v))
		} else {
			timeout = d
		}
	}

	maxTokens := llm.DefaultMaxTokens
	if v := strings.TrimSpace(os.Getenv("NABD_MAX_TOKENS")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 131072 {
			errs = append(errs, fmt.Errorf("invalid NABD_MAX_TOKENS %q: allowed range 1-131072", v))
		} else {
			maxTokens = n
		}
	}

	defaultTemp := llm.DefaultTemperature
	temp := &defaultTemp
	if v := strings.TrimSpace(os.Getenv("NABD_TEMPERATURE")); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 || f > 2 {
			errs = append(errs, fmt.Errorf("invalid NABD_TEMPERATURE %q: allowed range 0-2", v))
		} else {
			temp = &f
		}
	}

	if len(errs) > 0 {
		return llm.Config{}, "", errors.Join(errs...)
	}

	cfg := llm.Config{
		Provider:     provider,
		BaseURL:      baseURL,
		Model:        model,
		APIKey:       llm.Secret(apiKey),
		Timeout:      timeout,
		MaxTokens:    maxTokens,
		Temperature:  temp,
		GeminiSearch: geminiSearch,
	}

	return cfg, keySource, nil
}

type setupOptions struct {
	Stdin        io.Reader
	Stdout       io.Writer
	Stderr       io.Writer
	ConfigDir    string
	WorkspaceDir string
	Args         []string
	ReadSecret   func(prompt string) (string, error)
	IsTerminal   func() bool
}

func runSetupWithOptions(opts setupOptions) int {
	if len(opts.Args) > 0 {
		arg := opts.Args[0]
		switch arg {
		case "-h", "--help", "help":
			fmt.Fprintln(opts.Stdout, "Usage: nabd-agent setup [options]")
			fmt.Fprintln(opts.Stdout)
			fmt.Fprintln(opts.Stdout, "Configure LLM provider, endpoint, model, and API key securely.")
			fmt.Fprintln(opts.Stdout, "Non-secret settings are stored in config.json and API keys in")
			fmt.Fprintln(opts.Stdout, "dedicated 0600-permission files (api_key for OpenAI, gemini_api_key for Gemini).")
			fmt.Fprintln(opts.Stdout)
			fmt.Fprintln(opts.Stdout, "Options:")
			fmt.Fprintln(opts.Stdout, "  --reset    Remove saved configuration and API key files after confirmation")
			fmt.Fprintln(opts.Stdout, "  -h, --help Show this help message")
			return 0
		case "--reset":
			if opts.IsTerminal != nil && !opts.IsTerminal() {
				fmt.Fprintln(opts.Stderr, "error: interactive terminal required to reset configuration")
				return 1
			}
			fmt.Fprintf(opts.Stdout, "Are you sure you want to delete all saved NabdCoder configuration and API keys in %s? [y/N]: ", opts.ConfigDir)
			scanner := bufio.NewScanner(opts.Stdin)
			if scanner.Scan() {
				ans := strings.ToLower(strings.TrimSpace(scanner.Text()))
				if ans == "y" || ans == "yes" {
					if err := ResetConfig(opts.ConfigDir); err != nil {
						fmt.Fprintf(opts.Stderr, "Error resetting configuration: %v\n", err)
						return 1
					}
					fmt.Fprintln(opts.Stdout, "Saved configuration and keys successfully removed.")
					return 0
				}
			}
			fmt.Fprintln(opts.Stdout, "Reset cancelled.")
			return 0
		default:
			fmt.Fprintf(opts.Stderr, "unknown option: %s\nRun 'nabd-agent setup --help' for usage.\n", arg)
			return 1
		}
	}

	if opts.IsTerminal != nil && !opts.IsTerminal() {
		fmt.Fprintln(opts.Stderr, "error: interactive terminal required for setup")
		return 1
	}

	workspace := opts.WorkspaceDir
	if workspace == "" {
		if cwd, err := os.Getwd(); err == nil {
			workspace = cwd
		}
	}
	if err := validateConfigDirNotInWorkspace(opts.ConfigDir, workspace); err != nil {
		fmt.Fprintf(opts.Stderr, "Error: %v\n", err)
		return 1
	}

	existingCfg, _ := loadSavedConfig(opts.ConfigDir)
	existingOpenAIKey, _ := readSavedKey(filepath.Join(opts.ConfigDir, "api_key"))
	existingGeminiKey, _ := readSavedKey(filepath.Join(opts.ConfigDir, "gemini_api_key"))

	scanner := bufio.NewScanner(opts.Stdin)

	// 1. Provider
	defaultProvider := existingCfg.Provider
	if defaultProvider == "" {
		defaultProvider = llm.ProviderOpenAI
	}
	fmt.Fprintf(opts.Stdout, "Select provider [openai / gemini] (default: %s): ", defaultProvider)
	provider := defaultProvider
	if scanner.Scan() {
		t := strings.ToLower(strings.TrimSpace(scanner.Text()))
		if t != "" {
			provider = t
		}
	}
	if provider != llm.ProviderOpenAI && provider != llm.ProviderGemini {
		fmt.Fprintf(opts.Stderr, "error: invalid provider %q (must be 'openai' or 'gemini')\n", provider)
		return 1
	}

	// 2. Base URL
	defaultBaseURL := existingCfg.BaseURL
	if defaultBaseURL == "" {
		if provider == llm.ProviderGemini {
			defaultBaseURL = llm.DefaultGeminiBaseURL
		} else {
			defaultBaseURL = llm.DefaultBaseURL
		}
	}
	fmt.Fprintf(opts.Stdout, "Base URL (default: %s): ", defaultBaseURL)
	baseURL := defaultBaseURL
	if scanner.Scan() {
		t := strings.TrimSpace(scanner.Text())
		if t != "" {
			baseURL = t
		}
	}
	parsedURL, err := url.ParseRequestURI(baseURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		fmt.Fprintf(opts.Stderr, "error: invalid base URL %q: must be a valid http or https URL\n", baseURL)
		return 1
	}

	// 3. Model
	defaultModel := existingCfg.Model
	if defaultModel == "" {
		if provider == llm.ProviderGemini {
			defaultModel = llm.DefaultGeminiModel
		} else {
			defaultModel = llm.DefaultModel
		}
	}
	fmt.Fprintf(opts.Stdout, "Model name (default: %s): ", defaultModel)
	model := defaultModel
	if scanner.Scan() {
		t := strings.TrimSpace(scanner.Text())
		if t != "" {
			model = t
		}
	}

	// 4. API Key
	var existingKey string
	if provider == llm.ProviderGemini {
		existingKey = existingGeminiKey
	} else {
		existingKey = existingOpenAIKey
	}

	var promptMsg string
	if existingKey != "" {
		promptMsg = "Enter API key (press Enter to keep existing saved key): "
	} else {
		promptMsg = "Enter API key (input hidden): "
	}

	var apiKey string
	if opts.ReadSecret != nil {
		secret, err := opts.ReadSecret(promptMsg)
		if err != nil {
			fmt.Fprintf(opts.Stderr, "\nSetup cancelled: %v\n", err)
			return 1
		}
		apiKey = strings.TrimSpace(secret)
	} else {
		fmt.Fprint(opts.Stdout, promptMsg)
		if scanner.Scan() {
			apiKey = strings.TrimSpace(scanner.Text())
		}
	}

	if apiKey == "" {
		if existingKey != "" {
			apiKey = existingKey
		} else {
			fmt.Fprintln(opts.Stderr, "error: API key cannot be empty")
			return 1
		}
	}

	// 5. Connection Check
	fmt.Fprintf(opts.Stdout, "Test connection to %s (%s)? [Y/n]: ", provider, model)
	doTest := true
	if scanner.Scan() {
		ans := strings.ToLower(strings.TrimSpace(scanner.Text()))
		if ans == "n" || ans == "no" {
			doTest = false
		}
	}

	if doTest {
		fmt.Fprintln(opts.Stdout, "Testing connection...")
		testCfg := llm.Config{
			Provider: provider,
			BaseURL:  baseURL,
			Model:    model,
			APIKey:   llm.Secret(apiKey),
			Timeout:  10 * time.Second,
		}
		testClient, err := llm.NewClient(testCfg)
		if err != nil {
			fmt.Fprintf(opts.Stderr, "Failed to initialize client: %v\n", err)
			fmt.Fprint(opts.Stdout, "Save configuration anyway? [y/N]: ")
			if !scanner.Scan() || !strings.EqualFold(strings.TrimSpace(scanner.Text()), "y") {
				fmt.Fprintln(opts.Stdout, "Setup aborted.")
				return 1
			}
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if checker, ok := testClient.(interface{ CheckModel(context.Context) error }); ok {
				if err := checker.CheckModel(ctx); err != nil {
					fmt.Fprintf(opts.Stderr, "Connection test failed: %v\n", err)
					fmt.Fprint(opts.Stdout, "Save configuration anyway? [y/N]: ")
					if !scanner.Scan() || !strings.EqualFold(strings.TrimSpace(scanner.Text()), "y") {
						fmt.Fprintln(opts.Stdout, "Setup aborted.")
						return 1
					}
				} else {
					fmt.Fprintln(opts.Stdout, "✓ Connection verified successfully!")
				}
			}
		}
	}

	// 6. Save Configuration & Key
	savedCfg := SavedConfig{
		Provider: provider,
		BaseURL:  baseURL,
		Model:    model,
	}

	if err := SaveConfigAndKey(opts.ConfigDir, savedCfg, apiKey); err != nil {
		fmt.Fprintf(opts.Stderr, "Error saving configuration: %v\n", err)
		return 1
	}

	keyFile := keyFilenameForProvider(provider)
	fmt.Fprintln(opts.Stdout)
	fmt.Fprintf(opts.Stdout, "Configuration saved to: %s\n", filepath.Join(opts.ConfigDir, "config.json"))
	fmt.Fprintf(opts.Stdout, "API key saved to:       %s (permissions: 0600)\n", filepath.Join(opts.ConfigDir, keyFile))
	fmt.Fprintln(opts.Stdout)
	fmt.Fprintln(opts.Stdout, "[Security Notice]")
	fmt.Fprintln(opts.Stdout, "API keys are stored in plaintext on disk with restricted file permissions.")
	fmt.Fprintln(opts.Stdout, "They are not encrypted at rest. On Windows, 0600 does not configure NTFS ACLs;")
	fmt.Fprintln(opts.Stdout, "security relies on your user profile directory ACLs.")
	fmt.Fprintln(opts.Stdout, "Explicit environment variables (e.g. NABD_API_KEY) will always take precedence.")

	return 0
}

// RunSetup executes the interactive setup command using os.Stdin/os.Stdout/os.Stderr.
func RunSetup(stdin *os.File, stdout, stderr io.Writer, configDir string, args []string) int {
	return runSetupWithOptions(setupOptions{
		Stdin:     stdin,
		Stdout:    stdout,
		Stderr:    stderr,
		ConfigDir: configDir,
		Args:      args,
		IsTerminal: func() bool {
			return term.IsTerminal(int(stdin.Fd()))
		},
		ReadSecret: func(prompt string) (string, error) {
			fmt.Fprint(stdout, prompt)
			keyBytes, err := term.ReadPassword(int(stdin.Fd()))
			fmt.Fprintln(stdout)
			if err != nil {
				return "", err
			}
			return string(keyBytes), nil
		},
	})
}
