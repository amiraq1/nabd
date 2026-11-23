package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// MCPConfigEnv names the file of user-preconfigured MCP servers. There is no
// implicit default: with no config, mcp_call cannot resolve any server.
const MCPConfigEnv = "NABD_MCP_CONFIG"

var mcpAllowedEnvKeys = map[string]bool{
	"PATH": true, "HOME": true, "TMPDIR": true, "LANG": true,
	"LC_ALL": true, "TERM": true, "PREFIX": true,
}

type MCPServerConfig struct {
	Executable      string            `json:"executable"`
	Args            []string          `json:"args,omitempty"`
	SHA256          string            `json:"sha256"`
	Env             map[string]string `json:"env,omitempty"`
	Guard           string            `json:"guard"`
	GuardSHA256     string            `json:"guard_sha256"`
	UserGuard       string            `json:"user_guard,omitempty"`
	UserGuardSHA256 string            `json:"user_guard_sha256,omitempty"`
	UserGuardArgs   []string          `json:"user_guard_args,omitempty"`
}

type mcpConfigFile struct {
	Servers map[string]MCPServerConfig `json:"servers"`
}

// MCPServer is a validated, identity-bound server definition.
type MCPServer struct {
	Name     string
	Config   MCPServerConfig
	Identity string
}

// MCPConfig is the user-controlled allowlist. The model can only choose a name
// from this map; it cannot supply an executable path.
type MCPConfig struct {
	servers map[string]MCPServer
}

func digestFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("not an executable regular file: %s", path)
	}
	executable := info.Mode().Perm()&0111 != 0
	if runtime.GOOS == "windows" {
		// Windows reports no POSIX execute bits; executability is enforced by
		// the OS at launch time instead.
		executable = true
	}
	if !info.Mode().IsRegular() || !executable {
		return "", fmt.Errorf("not an executable regular file: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if int64(len(data)) != info.Size() {
		return "", fmt.Errorf("file changed while hashing: %s", path)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func validDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// LoadMCPConfig reads and validates a user-preconfigured server allowlist.
// Every executable and guard digest is checked at load time; a mismatch fails
// closed rather than downgrading to an unbound launch.
func LoadMCPConfig(path string) (*MCPConfig, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("MCP server allowlist is not configured; set %s", MCPConfigEnv)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("read MCP allowlist: %w", err)
	}
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("MCP allowlist is too large")
	}
	var file mcpConfigFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse MCP allowlist: %w", err)
	}
	if len(file.Servers) == 0 {
		return nil, fmt.Errorf("MCP allowlist has no servers")
	}
	cfg := &MCPConfig{servers: make(map[string]MCPServer, len(file.Servers))}
	for name, spec := range file.Servers {
		server, err := validateServer(name, spec)
		if err != nil {
			return nil, fmt.Errorf("server %q: %w", name, err)
		}
		cfg.servers[name] = server
	}
	return cfg, nil
}

func validateServer(name string, spec MCPServerConfig) (MCPServer, error) {
	if name == "" || strings.ContainsAny(name, "\x00\n\r\t") {
		return MCPServer{}, fmt.Errorf("invalid server name")
	}
	if !filepath.IsAbs(spec.Executable) {
		return MCPServer{}, fmt.Errorf("executable must be an absolute path")
	}
	if !validDigest(spec.SHA256) {
		return MCPServer{}, fmt.Errorf("invalid executable sha256")
	}
	if got, err := digestFile(spec.Executable); err != nil || got != spec.SHA256 {
		return MCPServer{}, fmt.Errorf("executable digest mismatch (err=%v)", err)
	}
	if !filepath.IsAbs(spec.Guard) || !validDigest(spec.GuardSHA256) {
		return MCPServer{}, fmt.Errorf("guard path and sha256 are required")
	}
	if got, err := digestFile(spec.Guard); err != nil || got != spec.GuardSHA256 {
		return MCPServer{}, fmt.Errorf("guard digest mismatch (err=%v)", err)
	}
	if spec.UserGuard != "" {
		if !filepath.IsAbs(spec.UserGuard) || !validDigest(spec.UserGuardSHA256) {
			return MCPServer{}, fmt.Errorf("user guard path and sha256 are required together")
		}
		if got, err := digestFile(spec.UserGuard); err != nil || got != spec.UserGuardSHA256 {
			return MCPServer{}, fmt.Errorf("user guard digest mismatch (err=%v)", err)
		}
	}
	for _, arg := range append(append([]string{}, spec.Args...), spec.UserGuardArgs...) {
		if strings.ContainsRune(arg, 0) {
			return MCPServer{}, fmt.Errorf("arguments cannot contain NUL")
		}
	}
	for key, value := range spec.Env {
		if !mcpAllowedEnvKeys[key] {
			return MCPServer{}, fmt.Errorf("environment key %q is not allowlisted", key)
		}
		if strings.ContainsAny(value, "\x00\n\r") {
			return MCPServer{}, fmt.Errorf("environment value contains a control character")
		}
	}
	server := MCPServer{Name: name, Config: spec}
	canonical, err := json.Marshal(struct {
		Name            string
		Executable      string
		Args            []string
		SHA256          string
		Env             []string
		Guard           string
		GuardSHA256     string
		UserGuard       string
		UserGuardSHA256 string
		UserGuardArgs   []string
	}{
		Name: name, Executable: spec.Executable, Args: spec.Args, SHA256: spec.SHA256,
		Env: sortedEnv(spec.Env), Guard: spec.Guard, GuardSHA256: spec.GuardSHA256,
		UserGuard: spec.UserGuard, UserGuardSHA256: spec.UserGuardSHA256, UserGuardArgs: spec.UserGuardArgs,
	})
	if err != nil {
		return MCPServer{}, err
	}
	sum := sha256.Sum256(canonical)
	server.Identity = hex.EncodeToString(sum[:])
	return server, nil
}

func sortedEnv(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+env[key])
	}
	return out
}

func (c *MCPConfig) Has(name string) bool {
	if c == nil {
		return false
	}
	_, ok := c.servers[name]
	return ok
}

// Resolve re-verifies every bound digest immediately before execution. This
// narrows (but does not eliminate) the window in which a local attacker could
// replace an executable; see the threat model for that residual risk.
func (c *MCPConfig) Resolve(name, expectedIdentity string) (MCPServer, error) {
	if c == nil {
		return MCPServer{}, fmt.Errorf("MCP server allowlist is not configured")
	}
	server, ok := c.servers[name]
	if !ok {
		return MCPServer{}, fmt.Errorf("MCP server %q is not in the user-configured allowlist", name)
	}
	if expectedIdentity != "" && server.Identity != expectedIdentity {
		return MCPServer{}, fmt.Errorf("MCP server identity changed since approval")
	}
	for _, check := range []struct {
		path string
		want string
		kind string
	}{
		{server.Config.Executable, server.Config.SHA256, "executable"},
		{server.Config.Guard, server.Config.GuardSHA256, "guard"},
	} {
		got, err := digestFile(check.path)
		if err != nil || got != check.want {
			return MCPServer{}, fmt.Errorf("MCP %s changed since configuration", check.kind)
		}
	}
	if server.Config.UserGuard != "" {
		got, err := digestFile(server.Config.UserGuard)
		if err != nil || got != server.Config.UserGuardSHA256 {
			return MCPServer{}, fmt.Errorf("MCP user guard changed since configuration")
		}
	}
	return server, nil
}

func (c *MCPConfig) Identity(name string) (string, error) {
	server, err := c.Resolve(name, "")
	if err != nil {
		return "", err
	}
	return server.Identity, nil
}

// MCPEnvironment builds the complete child environment from the server's
// explicit allowlist. Secrets in the parent environment are never inherited.
func MCPEnvironment(spec MCPServerConfig) []string {
	return sortedEnv(spec.Env)
}
