//go:build linux && (amd64 || arm64)

package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amiraq1/nabd/internal/sandbox"
)

// sandboxFixtureMarker is written into every host fixture so the probe can tell
// an exposed copy from a masked one.
const sandboxFixtureMarker = "NABD-SANDBOX-MARKER-7f3a"

// goVerifyProbeModule is the throwaway package that runs inside the sandbox. It
// records what it could read, write and reach, and writes the report back into
// its writable workspace so the host test can assert on it.
const goVerifyProbeModule = `package probe

// Marker keeps the package buildable and vet-able with -vet=all.
const Marker = "NABD-SANDBOX-MARKER-7f3a"
`

const goVerifyProbeTest = `package probe

import (
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

const marker = "NABD-SANDBOX-MARKER-7f3a"

func TestSandboxBoundaries(t *testing.T) {
	var report strings.Builder
	var violations []string

	for _, rel := range []string{".git/config", ".nabd/audit.log", ".env", "secret.key"} {
		data, err := os.ReadFile(rel)
		switch {
		case err != nil:
			report.WriteString("BLOCKED read " + rel + "\n")
		case !strings.Contains(string(data), marker):
			report.WriteString("BLOCKED read " + rel + "\n")
		default:
			report.WriteString("EXPOSED read " + rel + "\n")
			violations = append(violations, "read "+rel)
		}
	}

	for _, target := range []string{"/etc/nabd-probe", "/usr/nabd-probe", "/escape-probe"} {
		if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
			report.WriteString("BLOCKED write " + target + "\n")
			continue
		}
		report.WriteString("ALLOWED write " + target + "\n")
		violations = append(violations, "write "+target)
	}

	// /tmp is part of the shipped profile; the test asserts the real behaviour
	// instead of claiming that every path outside /workspace is unwritable.
	if err := os.WriteFile("/tmp/nabd-probe-tmp", []byte("x"), 0o600); err != nil {
		report.WriteString("BLOCKED write /tmp\n")
		violations = append(violations, "write /tmp")
	} else {
		report.WriteString("ALLOWED write /tmp\n")
	}

	if data, err := os.ReadFile("probe-net.txt"); err == nil {
		addr := strings.TrimSpace(string(data))
		conn, dialErr := net.DialTimeout("tcp", addr, 2*time.Second)
		if dialErr != nil {
			report.WriteString("BLOCKED dial " + addr + "\n")
		} else {
			_ = conn.Close()
			report.WriteString("ALLOWED dial " + addr + "\n")
			violations = append(violations, "dial "+addr)
		}
	} else {
		t.Fatalf("read probe-net.txt: %v", err)
	}

	if ns, err := os.Readlink("/proc/self/ns/net"); err == nil {
		report.WriteString("NETNS " + ns + "\n")
	} else {
		t.Fatalf("read netns: %v", err)
	}

	for _, violation := range violations {
		t.Errorf("sandbox boundary missing: %s", violation)
	}
	if err := os.WriteFile("probe-report.txt", []byte(report.String()), 0o600); err != nil {
		t.Fatalf("write probe report: %v", err)
	}
}
`

// sha256File digests an arbitrary workspace file. digestFile is the allowlist
// digest, which requires the executable bit, so it cannot be used on fixtures.
func sha256File(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// sandboxGoVerifySetup opens the execution gate on a throwaway workspace. It
// fails closed: a missing bwrap or a failed proof aborts the test instead of
// silently exercising the unsandboxed path.
func sandboxGoVerifySetup(t *testing.T) (*Registry, string) {
	t.Helper()
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bubblewrap is not installed; go_verify sandbox integration cannot be proven")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no Go toolchain is available to run inside the sandbox")
	}
	workspace := t.TempDir()
	helpers := t.TempDir()
	limiter := buildStaticHelper(t, helpers, "nabd-limit")
	probe := buildStaticHelper(t, helpers, "nabd-sandbox-probe")
	guard := buildStaticHelper(t, helpers, "mcp-guard")
	sandbox.SetHelpersForTesting(limiter, probe)
	t.Cleanup(func() { sandbox.SetHelpersForTesting("", "") })

	_, server := f24Probe(t, "env")
	serverSum, err := digestFile(server)
	if err != nil {
		t.Fatal(err)
	}
	guardSum, err := digestFile(guard)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := json.Marshal(mcpConfigFile{Servers: map[string]MCPServerConfig{
		"probe": {
			Executable:  server,
			SHA256:      serverSum,
			Guard:       guard,
			GuardSHA256: guardSum,
			Env:         map[string]string{"HOME": "/tmp", "PATH": "/usr/bin:/bin"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "allowlist.json")
	if err := os.WriteFile(cfgPath, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(MCPConfigEnv, cfgPath)
	t.Setenv("NABD_EXEC_SANDBOX", "1")

	r, err := NewRegistry(workspace)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if err := r.EnableExecution(context.Background()); err != nil {
		t.Fatalf("isolation gate did not open: %v", err)
	}
	return r, workspace
}

// TestGoVerifySandboxHidesWorkspaceSecrets runs the real go_verify path inside
// the real sandbox and proves the sensitive workspace entries stay hidden and
// byte-identical on the host.
func TestGoVerifySandboxHidesWorkspaceSecrets(t *testing.T) {
	r, workspace := sandboxGoVerifySetup(t)

	fixtures := map[string]string{
		".git/config":     "[core]\n\trepositoryformatversion = 0\n# " + sandboxFixtureMarker + "\n",
		".nabd/audit.log": `{"event":"requested","marker":"` + sandboxFixtureMarker + `"}` + "\n",
		".env":            "NABD_API_KEY=" + sandboxFixtureMarker + "\n",
		"secret.key":      sandboxFixtureMarker + "\n",
	}
	before := make(map[string]string, len(fixtures))
	for rel, content := range fixtures {
		path := filepath.Join(workspace, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		sum, err := sha256File(path)
		if err != nil {
			t.Fatal(err)
		}
		before[rel] = sum
	}

	if err := os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module nabdprobe\n\ngo 1.21\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "probe.go"), []byte(goVerifyProbeModule), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "probe_test.go"), []byte(goVerifyProbeTest), 0o600); err != nil {
		t.Fatal(err)
	}

	// Positive control outside the sandbox: without it, "connection refused"
	// inside would prove nothing about the network namespace.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	hostConn, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
	if err != nil {
		t.Fatalf("host loopback positive control failed: %v", err)
	}
	_ = hostConn.Close()
	if err := os.WriteFile(filepath.Join(workspace, "probe-net.txt"), []byte(ln.Addr().String()), 0o600); err != nil {
		t.Fatal(err)
	}
	hostNetNS, err := os.Readlink("/proc/self/ns/net")
	if err != nil || hostNetNS == "" {
		t.Fatalf("read host network namespace: %v", err)
	}

	args := json.RawMessage(`{"pkg":"."}`)
	approval, err := r.Approve("go_verify", args)
	if err != nil {
		t.Fatalf("approve go_verify: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	out := r.RunApproved(ctx, "go_verify", args, approval)
	if !strings.Contains(out, "PASS") {
		t.Fatalf("go_verify did not pass inside the sandbox: %s", out)
	}

	for rel, sum := range before {
		path := filepath.Join(workspace, filepath.FromSlash(rel))
		got, err := sha256File(path)
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		if got != sum {
			t.Errorf("%s changed on the host during sandboxed verification", rel)
		}
	}

	reportBytes, err := os.ReadFile(filepath.Join(workspace, "probe-report.txt"))
	if err != nil {
		t.Fatalf("the sandboxed probe left no report: %v\noutput: %s", err, out)
	}
	report := string(reportBytes)

	for _, rel := range []string{".git/config", ".nabd/audit.log", ".env", "secret.key"} {
		if !strings.Contains(report, "BLOCKED read "+rel) {
			t.Errorf("%s was reachable inside go_verify's sandbox: %s", rel, report)
		}
	}
	for _, target := range []string{"/etc/nabd-probe", "/usr/nabd-probe", "/escape-probe"} {
		if !strings.Contains(report, "BLOCKED write "+target) {
			t.Errorf("write to %s was not blocked: %s", target, report)
		}
	}
	if !strings.Contains(report, "ALLOWED write /tmp") {
		t.Errorf("/tmp is part of the shipped writable profile and must stay usable: %s", report)
	}
	if !strings.Contains(report, "BLOCKED dial "+ln.Addr().String()) {
		t.Errorf("the sandboxed process reached the host loopback listener: %s", report)
	}
	if !strings.Contains(report, "NETNS ") {
		t.Errorf("the probe could not report its network namespace: %s", report)
	}
	if strings.Contains(report, "NETNS "+hostNetNS) {
		t.Errorf("go_verify shared the host network namespace: %s", report)
	}
}
