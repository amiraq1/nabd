//go:build linux && !android

package sandbox

import (
	"bytes"
	"debug/elf"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
)

// maxLoaderDepth bounds the transitive dependency walk so a pathological
// library graph cannot make the bind list grow without limit.
const maxLoaderDepth = 8

// multiarchLibDir maps a Go architecture to the Debian multiarch triplet used
// by the shared library directories of common Linux distributions.
func multiarchLibDir(goarch string) string {
	switch goarch {
	case "amd64":
		return "x86_64-linux-gnu"
	case "arm64":
		return "aarch64-linux-gnu"
	case "386":
		return "i386-linux-gnu"
	case "arm":
		return "arm-linux-gnueabihf"
	case "ppc64le":
		return "powerpc64le-linux-gnu"
	case "s390x":
		return "s390x-linux-gnu"
	case "riscv64":
		return "riscv64-linux-gnu"
	}
	return ""
}

// librarySearchDirs lists the host directories the dynamic loader searches by
// default when no ld.so.cache is present inside the sandbox.
func librarySearchDirs() []string {
	dirs := []string{"/lib", "/usr/lib", "/lib64", "/usr/lib64"}
	if triplet := multiarchLibDir(runtime.GOARCH); triplet != "" {
		dirs = append(dirs, filepath.Join("/lib", triplet), filepath.Join("/usr/lib", triplet))
	}
	return dirs
}

// interpreterOf returns the ELF program interpreter, or "" for an executable
// that is statically linked.
func interpreterOf(f *elf.File) (string, error) {
	for _, prog := range f.Progs {
		if prog.Type != elf.PT_INTERP {
			continue
		}
		buf := make([]byte, prog.Filesz)
		if _, err := prog.ReadAt(buf, 0); err != nil {
			return "", fmt.Errorf("read ELF interpreter: %w", err)
		}
		return string(bytes.TrimRight(buf, "\x00")), nil
	}
	return "", nil
}

// importedLibraries returns the shared libraries an ELF file needs. A static
// file, or a file without a dynamic section, needs none.
func importedLibraries(path string) ([]string, error) {
	f, err := elf.Open(path)
	if err != nil {
		return nil, nil
	}
	defer f.Close()
	libs, err := f.ImportedLibraries()
	if err != nil {
		return nil, nil
	}
	return libs, nil
}

// resolveLibrary finds a shared library on the host by its DT_NEEDED name.
func resolveLibrary(name string) (string, bool) {
	if filepath.IsAbs(name) {
		return regularFile(name)
	}
	for _, dir := range librarySearchDirs() {
		if path, ok := regularFile(filepath.Join(dir, name)); ok {
			return path, true
		}
	}
	return "", false
}

func regularFile(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	return path, true
}

// LoaderBinds returns the host files a dynamically linked executable needs
// inside the sandbox: its ELF interpreter and every shared library it
// references, transitively resolved. Only individual files are returned, so
// the sandbox still exposes no /lib, /usr/lib or /etc directory.
//
// A statically linked executable needs nothing and yields no binds.
func LoaderBinds(executable string) ([]string, error) {
	f, err := os.Open(executable)
	if err != nil {
		return nil, err
	}
	elfFile, err := elf.NewFile(f)
	if err != nil {
		f.Close()
		// Not an ELF binary: there is no loader to bind.
		return nil, nil
	}
	// The file stays open: reading PT_INTERP and DT_NEEDED goes through it.
	defer elfFile.Close()

	interp, err := interpreterOf(elfFile)
	if err != nil {
		return nil, err
	}
	if interp == "" {
		return nil, nil
	}
	if !filepath.IsAbs(interp) {
		return nil, fmt.Errorf("dynamic loader path %q is not absolute", interp)
	}
	if _, ok := regularFile(interp); !ok {
		return nil, fmt.Errorf("dynamic loader %s is unavailable on this host", interp)
	}

	binds := []string{interp}
	seen := map[string]bool{interp: true}
	queue := []string{executable}
	for depth := 0; depth < maxLoaderDepth && len(queue) > 0; depth++ {
		var next []string
		for _, current := range queue {
			libs, err := importedLibraries(current)
			if err != nil {
				return nil, err
			}
			for _, name := range libs {
				path, ok := resolveLibrary(name)
				if !ok || seen[path] {
					continue
				}
				seen[path] = true
				binds = append(binds, path)
				next = append(next, path)
			}
		}
		queue = next
	}
	sort.Strings(binds[1:])
	return binds, nil
}
