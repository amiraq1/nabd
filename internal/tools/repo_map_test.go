package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestRegistryWithCleanup(t *testing.T, dir string) *Registry {
	t.Helper()
	reg, err := NewRegistry(dir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() {
		_ = reg.Close()
	})
	return reg
}

func TestRepoMap_BasicExtraction(t *testing.T) {
	dir := t.TempDir()

	// go.mod
	goMod := "module example.com/testmod\n\ngo 1.24\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o600); err != nil {
		t.Fatal(err)
	}

	// pkg/user/user.go
	userDir := filepath.Join(dir, "pkg", "user")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}

	userCode := `package user

import (
	"fmt"
	"example.com/testmod/pkg/sub"
	"github.com/external/pkg"
)

type UserID = int

type User struct {
	ID   UserID
	Name string
}

type Reader interface {
	Read(p []byte) (n int, err error)
}

func NewUser(name string) *User {
	fmt.Println("creating user")
	_ = sub.Something
	return &User{Name: name}
}

func (u *User) GetName() string {
	return u.Name
}

func (u User) GetID() UserID {
	return u.ID
}
`
	if err := os.WriteFile(filepath.Join(userDir, "user.go"), []byte(userCode), 0o600); err != nil {
		t.Fatal(err)
	}

	// user_test.go
	userTestCode := `package user

import "testing"

func TestUser(t *testing.T) {}
`
	if err := os.WriteFile(filepath.Join(userDir, "user_test.go"), []byte(userTestCode), 0o600); err != nil {
		t.Fatal(err)
	}

	// pkg/sub/sub.go
	subDir := filepath.Join(dir, "pkg", "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	subCode := `package sub

var Something = 42

type Config struct{}
`
	if err := os.WriteFile(filepath.Join(subDir, "sub.go"), []byte(subCode), 0o600); err != nil {
		t.Fatal(err)
	}

	reg := newTestRegistryWithCleanup(t, dir)

	res := reg.Run(context.Background(), "repo_map", json.RawMessage(`{}`))
	if strings.HasPrefix(res, "Error:") {
		t.Fatalf("unexpected error: %s", res)
	}

	// Check header
	if !strings.Contains(res, "=== REPO MAP (path: ., scanned: 2 files, symbols: 7, tests: 1) ===") {
		t.Errorf("header mismatch: %s", res)
	}
	if !strings.Contains(res, "module example.com/testmod") {
		t.Errorf("module name missing: %s", res)
	}

	// Check internal import displayed, external and stdlib not displayed
	if !strings.Contains(res, `import "example.com/testmod/pkg/sub"`) {
		t.Errorf("internal import missing: %s", res)
	}
	if strings.Contains(res, "github.com/external/pkg") {
		t.Errorf("external import should be hidden: %s", res)
	}
	if strings.Contains(res, `"fmt"`) {
		t.Errorf("stdlib import should be hidden: %s", res)
	}

	// Check symbols extracted
	expectedSymbols := []string{
		"type UserID = int",
		"type User struct",
		"type Reader interface",
		"func NewUser(string) *User",
		"method (*User) GetName() string",
		"method (User) GetID() UserID",
		"type Config struct",
	}
	for _, sym := range expectedSymbols {
		if !strings.Contains(res, sym) {
			t.Errorf("expected symbol %q not found in output:\n%s", sym, res)
		}
	}
}

func TestRepoMap_LineDirectiveIgnored(t *testing.T) {
	dir := t.TempDir()

	goMod := "module example.com/linepkg\n\ngo 1.24\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0o600); err != nil {
		t.Fatal(err)
	}

	// Line 1: package linepkg
	// Line 2: (empty)
	// Line 3: //line generated.go:9999
	// Line 4: type Foo struct{}
	code := "package linepkg\n\n//line generated.go:9999\ntype Foo struct{}\n"
	if err := os.WriteFile(filepath.Join(dir, "foo.go"), []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}

	reg := newTestRegistryWithCleanup(t, dir)

	res := reg.Run(context.Background(), "repo_map", json.RawMessage(`{}`))
	if strings.HasPrefix(res, "Error:") {
		t.Fatalf("unexpected error: %s", res)
	}

	// Must report physical line L4, NOT the //line directive L9999
	if strings.Contains(res, "L9999") {
		t.Errorf("found //line adjusted line L9999, expected physical line L4:\n%s", res)
	}
	if !strings.Contains(res, "type Foo struct L4") {
		t.Errorf("expected physical line 'type Foo struct L4' in output:\n%s", res)
	}
}

func TestRepoMap_MissingOrCorruptedGoMod(t *testing.T) {
	t.Run("missing go.mod", func(t *testing.T) {
		dir := t.TempDir()
		code := "package nomod\n\ntype Bar struct{}\n"
		if err := os.WriteFile(filepath.Join(dir, "bar.go"), []byte(code), 0o600); err != nil {
			t.Fatal(err)
		}

		reg := newTestRegistryWithCleanup(t, dir)
		res := reg.Run(context.Background(), "repo_map", json.RawMessage(`{}`))
		if strings.HasPrefix(res, "Error:") {
			t.Fatalf("unexpected error: %s", res)
		}

		if !strings.Contains(res, "module: unknown") {
			t.Errorf("expected 'module: unknown' when go.mod is missing:\n%s", res)
		}
	})

	t.Run("corrupted go.mod", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("this is not valid go mod"), 0o600); err != nil {
			t.Fatal(err)
		}
		code := "package corruptedmod\n\ntype Bar struct{}\n"
		if err := os.WriteFile(filepath.Join(dir, "bar.go"), []byte(code), 0o600); err != nil {
			t.Fatal(err)
		}

		reg := newTestRegistryWithCleanup(t, dir)
		res := reg.Run(context.Background(), "repo_map", json.RawMessage(`{}`))
		if strings.HasPrefix(res, "Error:") {
			t.Fatalf("unexpected error: %s", res)
		}

		if !strings.Contains(res, "module: unknown") {
			t.Errorf("expected 'module: unknown' when go.mod has no module directive:\n%s", res)
		}
	})
}

func TestRepoMap_DifferentPackagesInSameDir(t *testing.T) {
	dir := t.TempDir()

	file1 := "package alpha\n\ntype AlphaStruct struct{}\n"
	file2 := "package beta\n\ntype BetaStruct struct{}\n"

	if err := os.WriteFile(filepath.Join(dir, "alpha.go"), []byte(file1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "beta.go"), []byte(file2), 0o600); err != nil {
		t.Fatal(err)
	}

	reg := newTestRegistryWithCleanup(t, dir)
	res := reg.Run(context.Background(), "repo_map", json.RawMessage(`{}`))
	if strings.HasPrefix(res, "Error:") {
		t.Fatalf("unexpected error: %s", res)
	}

	// Must contain distinct package headers: one for alpha and one for beta
	if !strings.Contains(res, "pkg . (alpha, 1 files):") {
		t.Errorf("expected distinct package block for alpha:\n%s", res)
	}
	if !strings.Contains(res, "pkg . (beta, 1 files):") {
		t.Errorf("expected distinct package block for beta:\n%s", res)
	}
}

func TestRepoMap_InvalidGoFile(t *testing.T) {
	dir := t.TempDir()

	validCode := "package mypkg\n\ntype Good struct{}\n"
	invalidCode := "package mypkg\n\nfunc broken( { syntax error"

	if err := os.WriteFile(filepath.Join(dir, "good.go"), []byte(validCode), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.go"), []byte(invalidCode), 0o600); err != nil {
		t.Fatal(err)
	}

	reg := newTestRegistryWithCleanup(t, dir)
	res := reg.Run(context.Background(), "repo_map", json.RawMessage(`{}`))
	if strings.HasPrefix(res, "Error:") {
		t.Fatalf("unexpected overall error on invalid go file: %s", res)
	}

	// Should extract the good file's symbol
	if !strings.Contains(res, "type Good struct") {
		t.Errorf("expected Good struct in output:\n%s", res)
	}
	// Should report syntax error for bad.go gracefully
	if !strings.Contains(res, "syntax error") {
		t.Errorf("expected syntax error mention for bad.go:\n%s", res)
	}
}

func TestRepoMap_UnexportedSymbols(t *testing.T) {
	dir := t.TempDir()

	code := `package mypkg

type exported struct{}
type unexported struct{}

func ExportedFunc() {}
func unexportedFunc() {}
`
	if err := os.WriteFile(filepath.Join(dir, "file.go"), []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}

	reg := newTestRegistryWithCleanup(t, dir)

	// Default: unexported excluded
	resDefault := reg.Run(context.Background(), "repo_map", json.RawMessage(`{}`))
	if strings.Contains(resDefault, "unexported") {
		t.Errorf("unexported symbols should not appear by default:\n%s", resDefault)
	}
	if !strings.Contains(resDefault, "ExportedFunc") {
		t.Errorf("ExportedFunc should appear:\n%s", resDefault)
	}

	// Explicit include_unexported: true
	resWithUnexp := reg.Run(context.Background(), "repo_map", json.RawMessage(`{"include_unexported": true}`))
	if !strings.Contains(resWithUnexp, "unexported") {
		t.Errorf("unexported symbols should appear when include_unexported is true:\n%s", resWithUnexp)
	}
	if !strings.Contains(resWithUnexp, "unexportedFunc") {
		t.Errorf("unexportedFunc should appear:\n%s", resWithUnexp)
	}
}

func TestRepoMap_Determinism(t *testing.T) {
	dir := t.TempDir()

	// Create multiple directories and files out of alphabetical order
	for _, pkgName := range []string{"pkg_c", "pkg_a", "pkg_b"} {
		pkgDir := filepath.Join(dir, pkgName)
		if err := os.MkdirAll(pkgDir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, fileName := range []string{"z.go", "a.go", "m.go"} {
			code := fmt.Sprintf("package %s\n\ntype %s_%s struct{}\n", pkgName, strings.ToUpper(pkgName), strings.ToUpper(strings.TrimSuffix(fileName, ".go")))
			if err := os.WriteFile(filepath.Join(pkgDir, fileName), []byte(code), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	reg := newTestRegistryWithCleanup(t, dir)

	run1 := reg.Run(context.Background(), "repo_map", json.RawMessage(`{}`))
	run2 := reg.Run(context.Background(), "repo_map", json.RawMessage(`{}`))

	if run1 != run2 {
		t.Fatalf("outputs are not deterministic!\nRun1:\n%s\n\nRun2:\n%s", run1, run2)
	}
}

func TestRepoMap_BudgetCapAndTruncation(t *testing.T) {
	dir := t.TempDir()

	// Generate a large number of types/functions to easily exceed 12 KiB
	pkgDir := filepath.Join(dir, "largepkg")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var code strings.Builder
	code.WriteString("package largepkg\n\n")
	for i := 0; i < 400; i++ {
		code.WriteString(fmt.Sprintf("type MassiveDataStructureNumber%04d struct{}\n", i))
		code.WriteString(fmt.Sprintf("func ComputeComplexAlgorithmNumber%04d(arg1 string, arg2 int) (bool, error) { return true, nil }\n", i))
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "large.go"), []byte(code.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	reg := newTestRegistryWithCleanup(t, dir)

	res := reg.Run(context.Background(), "repo_map", json.RawMessage(`{}`))
	if strings.HasPrefix(res, "Error:") {
		t.Fatalf("unexpected error: %s", res)
	}

	if len(res) > maxRepoMapOutputBytes {
		t.Fatalf("output length %d exceeded max budget %d", len(res), maxRepoMapOutputBytes)
	}

	if !strings.Contains(res, "[truncated: repo_map output capped at 12 KiB") {
		t.Fatalf("expected truncation suffix in output, got length %d", len(res))
	}

	// Verify total count in header still counts all 800 symbols
	if !strings.Contains(res, "symbols: 800") {
		t.Errorf("header should preserve total symbol count (800) even when truncated:\n%s", res)
	}
}

func TestRepoMap_MaxFilesAndArgsValidation(t *testing.T) {
	dir := t.TempDir()
	reg := newTestRegistryWithCleanup(t, dir)

	// max_files < 1
	res := reg.Run(context.Background(), "repo_map", json.RawMessage(`{"max_files": 0}`))
	if !strings.Contains(res, "max_files must be between 1 and 400") {
		t.Errorf("expected validation error for max_files: 0, got: %s", res)
	}

	// max_files > 400
	res = reg.Run(context.Background(), "repo_map", json.RawMessage(`{"max_files": 401}`))
	if !strings.Contains(res, "max_files must be between 1 and 400") {
		t.Errorf("expected validation error for max_files: 401, got: %s", res)
	}

	// Unknown field
	res = reg.Run(context.Background(), "repo_map", json.RawMessage(`{"unknown_field": true}`))
	if !strings.Contains(res, "unknown field") {
		t.Errorf("expected unknown field error, got: %s", res)
	}
}

func TestRepoMap_SecurityPathEscape(t *testing.T) {
	dir := t.TempDir()
	reg := newTestRegistryWithCleanup(t, dir)

	// Outside workspace
	res := reg.Run(context.Background(), "repo_map", json.RawMessage(`{"path": ".."}`))
	if !strings.Contains(res, "path outside workspace not allowed") {
		t.Errorf("expected path escape error, got: %s", res)
	}

	// Sensitive directory
	res = reg.Run(context.Background(), "repo_map", json.RawMessage(`{"path": ".git"}`))
	if !strings.Contains(res, "access to sensitive path denied") {
		t.Errorf("expected sensitive path denied, got: %s", res)
	}
}

func TestRepoMap_ContextCancellation(t *testing.T) {
	dir := t.TempDir()
	reg := newTestRegistryWithCleanup(t, dir)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // canceled immediately

	res := reg.Run(ctx, "repo_map", json.RawMessage(`{}`))
	if !strings.Contains(res, "context canceled") {
		t.Errorf("expected context canceled error, got: %s", res)
	}
}

func TestRepoMap_SkipDirs(t *testing.T) {
	dir := t.TempDir()

	// vendor directory with Go file
	vendorDir := filepath.Join(dir, "vendor", "badpkg")
	if err := os.MkdirAll(vendorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vendorDir, "bad.go"), []byte("package badpkg\ntype VendorStruct struct{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// valid pkg
	goodDir := filepath.Join(dir, "goodpkg")
	if err := os.MkdirAll(goodDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goodDir, "good.go"), []byte("package goodpkg\ntype GoodStruct struct{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	reg := newTestRegistryWithCleanup(t, dir)
	res := reg.Run(context.Background(), "repo_map", json.RawMessage(`{}`))
	if strings.HasPrefix(res, "Error:") {
		t.Fatalf("unexpected error: %s", res)
	}

	if strings.Contains(res, "VendorStruct") {
		t.Errorf("vendor directory was not skipped:\n%s", res)
	}
	if !strings.Contains(res, "GoodStruct") {
		t.Errorf("GoodStruct should appear:\n%s", res)
	}
}

func TestRepoMap_PathComponentSymlink(t *testing.T) {
	dir := t.TempDir()
	targetDir := filepath.Join(dir, "target")
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(dir, "link_to_target")
	if err := os.Symlink(targetDir, symlinkPath); err != nil {
		// On Windows without privilege, skip
		t.Skipf("cannot create symlink: %v", err)
	}
	reg := newTestRegistryWithCleanup(t, dir)
	res := reg.Run(context.Background(), "repo_map", json.RawMessage(`{"path": "link_to_target"}`))
	if !strings.Contains(res, "symlink path components are not allowed") {
		t.Errorf("expected symlink path components error, got: %s", res)
	}
}
