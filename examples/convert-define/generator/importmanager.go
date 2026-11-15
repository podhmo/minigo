package generator

import (
	"fmt"
	"go/token"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
)

// ImportManager helps manage import statements for generated Go code.
type ImportManager struct {
	mu                 sync.Mutex
	currentPackagePath string
	imports            map[string]string // import path -> alias
	aliasesInUse       map[string]string // alias -> import path
}

var goKeywords = map[string]bool{
	"break":       true,
	"case":        true,
	"chan":        true,
	"const":       true,
	"continue":    true,
	"default":     true,
	"defer":       true,
	"else":        true,
	"fallthrough": true,
	"for":         true,
	"func":        true,
	"go":          true,
	"goto":        true,
	"if":          true,
	"import":      true,
	"interface":   true,
	"map":         true,
	"package":     true,
	"range":       true,
	"return":      true,
	"select":      true,
	"struct":      true,
	"switch":      true,
	"type":        true,
	"var":         true,
	// Common predeclared identifiers that might cause issues if used as package names
	"true":    true,
	"false":   true,
	"iota":    true,
	"nil":     true,
	"append":  true,
	"cap":     true,
	"close":   true,
	"complex": true,
	"copy":    true,
	"delete":  true,
	"imag":    true,
	"len":     true,
	"make":    true,
	"new":     true,
	"panic":   true,
	"print":   true,
	"println": true,
	"real":    true,
	"recover": true,
}

// NewImportManager creates a new ImportManager.
// currentPackagePath is the import path of the package being generated;
// types from it are emitted unqualified.
func NewImportManager(currentPackagePath string) *ImportManager {
	return &ImportManager{
		currentPackagePath: currentPackagePath,
		imports:            make(map[string]string),
		aliasesInUse:       make(map[string]string),
	}
}

// Add registers an import path and its desired alias.
// It handles conflicts by adjusting aliases if necessary.
// Returns the actual alias that should be used for the package.
// If the path is the current package's path, it returns an empty string (no alias needed for qualification).
func (im *ImportManager) Add(path string, requestedAlias string) string {
	if path == "" {
		return ""
	}
	if im.currentPackagePath != "" && path == im.currentPackagePath {
		return ""
	}

	im.mu.Lock()
	defer im.mu.Unlock()

	if alias, ok := im.imports[path]; ok {
		return alias
	}

	var aliasCandidate string
	if requestedAlias == "" {
		aliasCandidate = filepath.Base(path)
		aliasCandidate = strings.ReplaceAll(aliasCandidate, "-", "_")
		aliasCandidate = strings.ReplaceAll(aliasCandidate, ".", "_")
	} else {
		aliasCandidate = requestedAlias
		aliasCandidate = strings.ReplaceAll(aliasCandidate, "-", "_")
		aliasCandidate = strings.ReplaceAll(aliasCandidate, ".", "_")
	}

	if goKeywords[aliasCandidate] {
		aliasCandidate += "_pkg"
	}

	// Now, ensure the (potentially keyword-adjusted) alias is a valid identifier.
	// "_" is a valid identifier but only ever a blank import — Qualify
	// would emit "_.Type", so it takes the fallback path like a bad name.
	if aliasCandidate == "" || aliasCandidate == "_" || !token.IsIdentifier(aliasCandidate) {
		// If path itself was something like ".", base becomes "." then "_", then invalid.
		// Or if user provided "123", it becomes invalid.
		// Or if keyword adjustment made it invalid (unlikely for "_pkg" suffix).

		if !(strings.HasPrefix(aliasCandidate, "pkg_") && len(aliasCandidate) > 4) {
			aliasCandidate = "pkg_" + aliasCandidate
		}

		if aliasCandidate == "pkg_" {
			var h uint32
			for _, r := range path {
				h = h*31 + uint32(r)
			}
			aliasCandidate = fmt.Sprintf("p%x", h)
			if goKeywords[aliasCandidate] {
				aliasCandidate += "_pkg"
			}
		}
	}

	finalAlias := aliasCandidate
	counter := 1
	for {
		existingPathForAlias, aliasInUse := im.aliasesInUse[finalAlias]
		isKeyword := goKeywords[finalAlias]

		if !isKeyword && (!aliasInUse || existingPathForAlias == path) {
			break
		}

		finalAlias = fmt.Sprintf("%s%d", aliasCandidate, counter)
		counter++
		if counter > 100 {
			pathParts := strings.Split(path, "/")
			var uniquePart string
			if len(pathParts) > 1 {
				uniquePart = strings.ReplaceAll(pathParts[len(pathParts)-2], "-", "_")
				uniquePart = strings.ReplaceAll(uniquePart, ".", "_")
			} else {
				uniquePart = "p"
			}
			baseHashAlias := uniquePart + "_" + filepath.Base(path)
			baseHashAlias = strings.ReplaceAll(baseHashAlias, "-", "_")
			baseHashAlias = strings.ReplaceAll(baseHashAlias, ".", "_")

			finalAlias = baseHashAlias
			altCounter := 1
			for {
				ep, iu := im.aliasesInUse[finalAlias]
				gk := goKeywords[finalAlias]
				if (!iu || ep == path) && !gk {
					break
				}
				finalAlias = fmt.Sprintf("%s%d", baseHashAlias, altCounter)
				altCounter++
				if altCounter > 20 {
					slog.Error("Failed to generate a unique alias after multiple fallbacks", "path", path, "requestedAlias", requestedAlias)
					return path // Return the path itself as a last resort (will likely cause compile error, but signals problem)
				}
			}
			slog.Warn(fmt.Sprintf("High alias conflict count for path %s with base %s. Using generated alias: %s", path, aliasCandidate, finalAlias))
			break
		}
	}

	im.imports[path] = finalAlias
	if finalAlias != "" { // Don't record an empty alias as "in use" for qualification purposes
		im.aliasesInUse[finalAlias] = path
	}
	return finalAlias
}

// Qualify returns the qualified type name (e.g., "alias.TypeName" or "TypeName").
// It ensures the package path is registered with an alias.
// If packagePath is empty or matches currentPackagePath, typeName is returned as is.
func (im *ImportManager) Qualify(packagePath string, typeName string) string {
	if packagePath == "" || (im.currentPackagePath != "" && packagePath == im.currentPackagePath) {
		return typeName // Type is in current package, built-in, or no package path given
	}

	im.mu.Lock()
	alias, ok := im.imports[packagePath]
	im.mu.Unlock()

	if !ok {
		requestedAliasHint := ""
		if packagePath != "" {
			requestedAliasHint = filepath.Base(packagePath)
		}
		alias = im.Add(packagePath, requestedAliasHint)
	}

	if alias == "" { // This implies it's the current package (handled by Add) or an error/empty path.
		return typeName
	}
	return alias + "." + typeName
}

// Imports returns a copy of the registered import-path-to-alias map.
// Entries with empty aliases are excluded.
func (im *ImportManager) Imports() map[string]string {
	im.mu.Lock()
	defer im.mu.Unlock()

	importsCopy := make(map[string]string, len(im.imports))
	for path, alias := range im.imports {
		// We generally want all registered imports for the `import (...)` block.
		// `Add` should ensure that `currentPackagePath` isn't in `im.imports` to avoid `import . "current/pkg"`.
		// If an alias is intentionally set to "_" (blank identifier), it should still be included.
		importsCopy[path] = alias
	}
	return importsCopy
}
