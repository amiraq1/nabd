package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	maxRepoMapOutputBytes = 12 * 1024 // 12 KiB budget
	maxRepoMapFilesLimit  = 400
	defaultRepoMapFiles   = 100
	maxRepoMapFileBytes   = 256 << 10 // 256 KiB per Go file
	maxRepoMapTotalBytes  = 10 << 20  // 10 MiB total read budget
	maxRepoMapDirEntries  = 4096      // max total directory entries scanned
	maxRepoMapDepth       = 15        // max directory recursion depth
	repoMapTruncSuffix    = "\n... [truncated: repo_map output capped at 12 KiB; use a more specific path or decrease max_files]"
)

type repoMapArgs struct {
	Path              string `json:"path"`
	IncludeUnexported bool   `json:"include_unexported"`
	MaxFiles          *int   `json:"max_files"`
}

func parseRepoMapArgs(args json.RawMessage) (repoMapArgs, error) {
	var p repoMapArgs
	if len(args) == 0 || string(args) == "null" {
		p.Path = "."
		return p, nil
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("invalid repo_map arguments: %w", err)
	}
	if strings.TrimSpace(p.Path) == "" {
		p.Path = "."
	}
	if p.MaxFiles != nil {
		if *p.MaxFiles < 1 || *p.MaxFiles > maxRepoMapFilesLimit {
			return p, fmt.Errorf("max_files must be between 1 and %d (received %d)", maxRepoMapFilesLimit, *p.MaxFiles)
		}
	}
	return p, nil
}

// validateRepoMapPath checks that rawPath is safe, resides in the workspace root,
// and verifies that none of its path components is a symlink.
// Note on TOCTOU: Although checking components via Lstat detects symlinks at check time,
// a concurrent modification could replace a path component before opening.
// In NabdCoder, os.Root enforces containment at open time as the primary barrier.
func (r *Registry) validateRepoMapPath(rawPath string) (string, error) {
	rel, err := r.resolveSafePath(rawPath)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return ".", nil
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	cur := ""
	for _, part := range parts {
		if cur == "" {
			cur = part
		} else {
			cur = cur + "/" + part
		}
		info, err := r.root.Lstat(cur)
		if err != nil {
			return "", fmt.Errorf("stat path component %q: %w", cur, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink path components are not allowed: %s", cur)
		}
	}
	return rel, nil
}

func shouldSkipRepoMapDir(name string) bool {
	switch name {
	case ".git", "vendor", "node_modules", ".nabd", ".ua", "dist", "build":
		return true
	default:
		return false
	}
}

func readWorkspaceModuleName(root *os.Root) string {
	f, err := openRegularSingleLink(root, "go.mod")
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 16<<10))
	if err != nil {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			mod := strings.TrimSpace(strings.TrimPrefix(line, "module "))
			if idx := strings.Index(mod, "//"); idx >= 0 {
				mod = strings.TrimSpace(mod[:idx])
			}
			return strings.Trim(mod, "`\"'")
		}
	}
	return ""
}

func isInternalImport(impPath, moduleName string) bool {
	if moduleName == "" {
		return false
	}
	impPath = strings.Trim(impPath, `"`)
	return impPath == moduleName || strings.HasPrefix(impPath, moduleName+"/")
}

type symbolInfo struct {
	text string
	line int
	name string
}

type fileSummary struct {
	relPath         string
	fileName        string
	internalImports []string
	symbols         []symbolInfo
	syntaxErr       string
	skipped         string
}

type packageKey struct {
	dirPath string
	pkgName string
}

type packageGroup struct {
	dirPath string
	pkgName string
	files   []*fileSummary
	tests   int
}

func formatTypeExpr(expr ast.Expr) string {
	if expr == nil {
		return ""
	}
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + formatTypeExpr(t.X)
	case *ast.SelectorExpr:
		return formatTypeExpr(t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + formatTypeExpr(t.Elt)
		}
		return "[...]" + formatTypeExpr(t.Elt)
	case *ast.MapType:
		return "map[" + formatTypeExpr(t.Key) + "]" + formatTypeExpr(t.Value)
	case *ast.InterfaceType:
		return "interface{}"
	case *ast.FuncType:
		return "func(...)"
	case *ast.ChanType:
		return "chan " + formatTypeExpr(t.Value)
	case *ast.Ellipsis:
		return "..." + formatTypeExpr(t.Elt)
	default:
		return "..."
	}
}

func formatReceiver(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return "(*" + formatTypeExpr(t.X) + ")"
	case *ast.Ident:
		return "(" + t.Name + ")"
	case *ast.IndexExpr:
		return "(" + formatTypeExpr(t.X) + "[...])"
	case *ast.IndexListExpr:
		return "(" + formatTypeExpr(t.X) + "[...])"
	default:
		return "(recv)"
	}
}

func formatFuncSignature(fnType *ast.FuncType) string {
	var params []string
	if fnType.Params != nil {
		for _, field := range fnType.Params.List {
			typeStr := formatTypeExpr(field.Type)
			if len(field.Names) > 0 {
				for range field.Names {
					params = append(params, typeStr)
				}
			} else {
				params = append(params, typeStr)
			}
		}
	}
	var results []string
	if fnType.Results != nil {
		for _, field := range fnType.Results.List {
			typeStr := formatTypeExpr(field.Type)
			if len(field.Names) > 0 {
				for range field.Names {
					results = append(results, typeStr)
				}
			} else {
				results = append(results, typeStr)
			}
		}
	}
	ret := "(" + strings.Join(params, ", ") + ")"
	if len(results) == 1 {
		ret += " " + results[0]
	} else if len(results) > 1 {
		ret += " (" + strings.Join(results, ", ") + ")"
	}
	return ret
}

func parseTestPackageName(root *os.Root, testPath string) string {
	f, err := openRegularSingleLink(root, testPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	src, err := io.ReadAll(io.LimitReader(f, 4<<10))
	if err != nil {
		return ""
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, testPath, src, parser.PackageClauseOnly)
	if err != nil || file == nil || file.Name == nil {
		return ""
	}
	return file.Name.Name
}

func applyRepoMapBudget(content string) string {
	if len(content) <= maxRepoMapOutputBytes {
		return content
	}
	cutoff := maxRepoMapOutputBytes - len(repoMapTruncSuffix)
	if cutoff < 0 {
		cutoff = 0
	}
	lastNL := strings.LastIndex(content[:cutoff], "\n")
	if lastNL > 0 {
		cutoff = lastNL
	} else {
		for cutoff > 0 && !utf8.RuneStart(content[cutoff]) {
			cutoff--
		}
	}
	return content[:cutoff] + repoMapTruncSuffix
}

// repoMap implements the repo_map tool.
func (r *Registry) repoMap(ctx context.Context, args json.RawMessage) (string, error) {
	p, err := parseRepoMapArgs(args)
	if err != nil {
		return "", err
	}
	relPath, err := r.validateRepoMapPath(p.Path)
	if err != nil {
		return "", err
	}

	maxFiles := defaultRepoMapFiles
	if p.MaxFiles != nil {
		maxFiles = *p.MaxFiles
	}

	moduleName := readWorkspaceModuleName(r.root)

	// Check if target is a single file
	targetInfo, err := r.root.Stat(relPath)
	if err != nil {
		return "", err
	}

	var goFiles []string
	var testFiles []string

	if !targetInfo.IsDir() {
		if strings.HasSuffix(relPath, "_test.go") {
			testFiles = append(testFiles, relPath)
		} else if strings.HasSuffix(relPath, ".go") {
			goFiles = append(goFiles, relPath)
		}
	} else {
		// Traverse directories
		type dirWork struct {
			rel   string
			depth int
		}
		queue := []dirWork{{rel: relPath, depth: 0}}
		scannedEntries := 0

		for len(queue) > 0 && scannedEntries < maxRepoMapDirEntries && len(goFiles) < maxFiles {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			curr := queue[0]
			queue = queue[1:]

			f, err := openWorkspaceFile(r.root, curr.rel)
			if err != nil {
				continue
			}
			entries, err := f.ReadDir(-1)
			_ = f.Close()
			if err != nil {
				continue
			}

			sort.Slice(entries, func(i, j int) bool {
				return entries[i].Name() < entries[j].Name()
			})

			for _, e := range entries {
				scannedEntries++
				if scannedEntries > maxRepoMapDirEntries {
					break
				}
				name := e.Name()
				if e.Type()&os.ModeSymlink != 0 {
					continue
				}
				childRel := filepath.Join(curr.rel, name)
				if isSensitivePath(childRel) {
					continue
				}

				if e.IsDir() {
					if shouldSkipRepoMapDir(name) {
						continue
					}
					if curr.depth < maxRepoMapDepth {
						queue = append(queue, dirWork{rel: childRel, depth: curr.depth + 1})
					}
				} else {
					if strings.HasSuffix(name, "_test.go") {
						testFiles = append(testFiles, childRel)
					} else if strings.HasSuffix(name, ".go") {
						if len(goFiles) < maxFiles {
							goFiles = append(goFiles, childRel)
						}
					}
				}
			}
		}
	}

	sort.Strings(goFiles)
	sort.Strings(testFiles)

	// Tally discovered tests per package/directory
	pkgTests := make(map[packageKey]int)
	totalTests := len(testFiles)
	for _, tf := range testFiles {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		dir := filepath.Dir(tf)
		pkgName := parseTestPackageName(r.root, tf)
		if pkgName != "" {
			pkgTests[packageKey{dirPath: dir, pkgName: pkgName}]++
		} else {
			pkgTests[packageKey{dirPath: dir, pkgName: ""}]++
		}
	}

	// Parse Go files
	pkgGroupsMap := make(map[packageKey]*packageGroup)
	var totalBytesRead int64
	totalSymbols := 0
	totalScannedFiles := 0

	for _, gf := range goFiles {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if totalBytesRead >= maxRepoMapTotalBytes {
			break
		}

		totalScannedFiles++
		dir := filepath.Dir(gf)
		baseName := filepath.Base(gf)

		f, err := openRegularSingleLink(r.root, gf)
		if err != nil {
			k := packageKey{dirPath: dir, pkgName: "(unreadable)"}
			if pkgGroupsMap[k] == nil {
				pkgGroupsMap[k] = &packageGroup{dirPath: dir, pkgName: "(unreadable)"}
			}
			pkgGroupsMap[k].files = append(pkgGroupsMap[k].files, &fileSummary{
				relPath:  gf,
				fileName: baseName,
				skipped:  fmt.Sprintf("open failed: %v", err),
			})
			continue
		}

		data, err := io.ReadAll(io.LimitReader(f, maxRepoMapFileBytes+1))
		_ = f.Close()
		if err != nil {
			k := packageKey{dirPath: dir, pkgName: "(unreadable)"}
			if pkgGroupsMap[k] == nil {
				pkgGroupsMap[k] = &packageGroup{dirPath: dir, pkgName: "(unreadable)"}
			}
			pkgGroupsMap[k].files = append(pkgGroupsMap[k].files, &fileSummary{
				relPath:  gf,
				fileName: baseName,
				skipped:  fmt.Sprintf("read failed: %v", err),
			})
			continue
		}

		if len(data) > maxRepoMapFileBytes {
			k := packageKey{dirPath: dir, pkgName: "(oversized)"}
			if pkgGroupsMap[k] == nil {
				pkgGroupsMap[k] = &packageGroup{dirPath: dir, pkgName: "(oversized)"}
			}
			pkgGroupsMap[k].files = append(pkgGroupsMap[k].files, &fileSummary{
				relPath:  gf,
				fileName: baseName,
				skipped:  "file exceeds 256 KiB limit, skipped",
			})
			continue
		}

		totalBytesRead += int64(len(data))

		fset := token.NewFileSet()
		node, parseErr := parser.ParseFile(fset, gf, data, parser.SkipObjectResolution)
		if parseErr != nil && node == nil {
			k := packageKey{dirPath: dir, pkgName: "(syntax_error)"}
			if pkgGroupsMap[k] == nil {
				pkgGroupsMap[k] = &packageGroup{dirPath: dir, pkgName: "(syntax_error)"}
			}
			pkgGroupsMap[k].files = append(pkgGroupsMap[k].files, &fileSummary{
				relPath:   gf,
				fileName:  baseName,
				syntaxErr: parseErr.Error(),
			})
			continue
		}

		pkgName := "(unknown)"
		if node != nil && node.Name != nil {
			pkgName = node.Name.Name
		}

		k := packageKey{dirPath: dir, pkgName: pkgName}
		if pkgGroupsMap[k] == nil {
			pkgGroupsMap[k] = &packageGroup{dirPath: dir, pkgName: pkgName}
		}

		fs := &fileSummary{
			relPath:  gf,
			fileName: baseName,
		}

		if parseErr != nil {
			fs.syntaxErr = parseErr.Error()
		}

		if node != nil {
			for _, imp := range node.Imports {
				if imp.Path != nil {
					val := strings.Trim(imp.Path.Value, `"`)
					if isInternalImport(val, moduleName) {
						fs.internalImports = append(fs.internalImports, val)
					}
				}
			}
			sort.Strings(fs.internalImports)

			for _, decl := range node.Decls {
				switch d := decl.(type) {
				case *ast.GenDecl:
					if d.Tok == token.TYPE {
						for _, spec := range d.Specs {
							ts, ok := spec.(*ast.TypeSpec)
							if !ok {
								continue
							}
							name := ts.Name.Name
							if !p.IncludeUnexported && !ast.IsExported(name) {
								continue
							}
							line := fset.PositionFor(ts.Pos(), false).Line
							if ts.Assign.IsValid() {
								target := formatTypeExpr(ts.Type)
								fs.symbols = append(fs.symbols, symbolInfo{
									text: fmt.Sprintf("type %s = %s L%d", name, target, line),
									line: line,
									name: name,
								})
								totalSymbols++
							} else {
								kind := formatTypeExpr(ts.Type)
								switch ts.Type.(type) {
								case *ast.StructType:
									kind = "struct"
								case *ast.InterfaceType:
									kind = "interface"
								}
								fs.symbols = append(fs.symbols, symbolInfo{
									text: fmt.Sprintf("type %s %s L%d", name, kind, line),
									line: line,
									name: name,
								})
								totalSymbols++
							}
						}
					}
				case *ast.FuncDecl:
					name := d.Name.Name
					if !p.IncludeUnexported && !ast.IsExported(name) {
						continue
					}
					line := fset.PositionFor(d.Pos(), false).Line
					sig := formatFuncSignature(d.Type)
					if d.Recv != nil && len(d.Recv.List) > 0 {
						recvStr := formatReceiver(d.Recv.List[0].Type)
						fs.symbols = append(fs.symbols, symbolInfo{
							text: fmt.Sprintf("method %s %s%s L%d", recvStr, name, sig, line),
							line: line,
							name: name,
						})
						totalSymbols++
					} else {
						fs.symbols = append(fs.symbols, symbolInfo{
							text: fmt.Sprintf("func %s%s L%d", name, sig, line),
							line: line,
							name: name,
						})
						totalSymbols++
					}
				}
			}
			sort.Slice(fs.symbols, func(i, j int) bool {
				if fs.symbols[i].line != fs.symbols[j].line {
					return fs.symbols[i].line < fs.symbols[j].line
				}
				return fs.symbols[i].name < fs.symbols[j].name
			})
		}

		pkgGroupsMap[k].files = append(pkgGroupsMap[k].files, fs)
	}

	// Associate tests with package groups
	for k, grp := range pkgGroupsMap {
		grp.tests += pkgTests[k]
		// Also credit foo_test tests to package foo in the same directory
		testExtKey := packageKey{dirPath: k.dirPath, pkgName: k.pkgName + "_test"}
		grp.tests += pkgTests[testExtKey]
	}

	groups := make([]*packageGroup, 0, len(pkgGroupsMap))
	for _, grp := range pkgGroupsMap {
		groups = append(groups, grp)
	}

	sort.Slice(groups, func(i, j int) bool {
		if groups[i].dirPath != groups[j].dirPath {
			return groups[i].dirPath < groups[j].dirPath
		}
		return groups[i].pkgName < groups[j].pkgName
	})

	for _, grp := range groups {
		sort.Slice(grp.files, func(i, j int) bool {
			return grp.files[i].fileName < grp.files[j].fileName
		})
	}

	var b strings.Builder
	dispPath := filepath.ToSlash(p.Path)
	b.WriteString(fmt.Sprintf("=== REPO MAP (path: %s, scanned: %d files, symbols: %d, tests: %d) ===\n",
		dispPath, totalScannedFiles, totalSymbols, totalTests))
	if moduleName != "" {
		b.WriteString(fmt.Sprintf("module %s\n\n", moduleName))
	} else {
		b.WriteString("module: unknown\n\n")
	}

	if len(groups) == 0 {
		b.WriteString("(no Go files found)\n")
		return applyRepoMapBudget(b.String()), nil
	}

	for _, grp := range groups {
		dirDisplay := filepath.ToSlash(grp.dirPath)
		if dirDisplay == "" {
			dirDisplay = "."
		}
		testSuffix := ""
		if grp.tests > 0 {
			testSuffix = fmt.Sprintf(", %d tests", grp.tests)
		}
		b.WriteString(fmt.Sprintf("pkg %s (%s, %d files%s):\n", dirDisplay, grp.pkgName, len(grp.files), testSuffix))
		for _, f := range grp.files {
			b.WriteString(fmt.Sprintf("  %s:\n", f.fileName))
			if f.skipped != "" {
				b.WriteString(fmt.Sprintf("    (%s)\n", f.skipped))
				continue
			}
			if f.syntaxErr != "" {
				b.WriteString(fmt.Sprintf("    [syntax error: %s]\n", f.syntaxErr))
				continue
			}
			for _, imp := range f.internalImports {
				b.WriteString(fmt.Sprintf("    import %q\n", imp))
			}
			if len(f.symbols) == 0 {
				if p.IncludeUnexported {
					b.WriteString("    (no symbols)\n")
				} else {
					b.WriteString("    (no exported symbols)\n")
				}
			} else {
				for _, sym := range f.symbols {
					b.WriteString(fmt.Sprintf("    %s\n", sym.text))
				}
			}
		}
		b.WriteString("\n")
	}

	return applyRepoMapBudget(b.String()), nil
}
