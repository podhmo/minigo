package goscan

import (
	"fmt"
	"go/token"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"

	"github.com/podhmo/minigo/examples/convert-define/pkg/scanner"
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
// currentPkgInfo is the package information for the file being generated.
// If currentPkgInfo is nil, the ImportManager assumes no specific current package context.
func NewImportManager(currentPkgInfo *scanner.PackageInfo) *ImportManager {
	im := &ImportManager{
		imports:      make(map[string]string),
		aliasesInUse: make(map[string]string),
	}
	if currentPkgInfo != nil {
		im.currentPackagePath = currentPkgInfo.ImportPath
	}
	return im
}

// Add registers an import path and its desired alias.
// It handles conflicts by adjusting aliases if necessary.
// Returns the actual alias that should be used for the package.
// If the path is the current package's path, it returns an empty string (no alias needed for qualification).
func (im *ImportManager) Add(path string, requestedAlias string) string {
	if path == "" {
		return "" // Cannot import an empty path
	}
	if im.currentPackagePath != "" && path == im.currentPackagePath {
		return "" // Return empty string to signify no alias needed for qualification
	}

	im.mu.Lock()
	defer im.mu.Unlock()

	// Check if this path is already imported
	if alias, ok := im.imports[path]; ok {
		return alias // Return existing alias
	}

	// Determine initial aliasCandidate
	var aliasCandidate string
	if requestedAlias == "" {
		aliasCandidate = filepath.Base(path)
		aliasCandidate = strings.ReplaceAll(aliasCandidate, "-", "_")
		aliasCandidate = strings.ReplaceAll(aliasCandidate, ".", "_")
	} else {
		aliasCandidate = requestedAlias
		// Also sanitize user-provided alias
		aliasCandidate = strings.ReplaceAll(aliasCandidate, "-", "_")
		aliasCandidate = strings.ReplaceAll(aliasCandidate, ".", "_")
	}

	// First, check if the sanitized base/requested name is a keyword.
	// If so, append "_pkg" immediately.
	if goKeywords[aliasCandidate] {
		aliasCandidate += "_pkg"
	}

	// Now, ensure the (potentially keyword-adjusted) alias is a valid identifier.
	if aliasCandidate == "" || !token.IsIdentifier(aliasCandidate) {
		// If path itself was something like ".", base becomes "." then "_", then invalid.
		// Or if user provided "123", it becomes invalid.
		// Or if keyword adjustment made it invalid (unlikely for "_pkg" suffix).

		// Avoid double prefixing if it already somehow starts with pkg_ from a bad state
		if !(strings.HasPrefix(aliasCandidate, "pkg_") && len(aliasCandidate) > 4) {
			aliasCandidate = "pkg_" + aliasCandidate
		}

		// If aliasCandidate became "pkg_" or (it was like "pkg_keyword" and keyword was removed, leaving "pkg_"), it means original was problematic.
		// Create a more unique fallback.
		// A more robust check for "pkg_keyword" being reduced to "pkg_" needs to consider the original keyword.
		// For simplicity, just check if it's "pkg_".
		if aliasCandidate == "pkg_" {
			var h uint32
			for _, r := range path {
				h = h*31 + uint32(r)
			}
			aliasCandidate = fmt.Sprintf("p%x", h) // Use a short hash of the path
			// Re-check keyword for the hashed version, though extremely unlikely
			if goKeywords[aliasCandidate] { // Check keyword again for the hashed version
				aliasCandidate += "_pkg"
			}
		}
	}
	// It's possible that aliasCandidate is now a keyword_pkg which is fine,
	// or pkg_nonkeyword which is fine, or p<hash> which is fine, or p<hash>_pkg if hash was a keyword.
	// A final explicit check for keyword status on `finalAlias` inside the conflict resolution loop
	// is already present and should ensure safety.

	// Handle alias conflicts with existing aliases
	finalAlias := aliasCandidate
	counter := 1
	for {
		existingPathForAlias, aliasInUse := im.aliasesInUse[finalAlias]
		isKeyword := goKeywords[finalAlias] // Keyword check for the current finalAlias iteration

		// Break condition:
		// 1. It's NOT a keyword AND
		// 2. EITHER it's not in use OR it's in use by the current path we are trying to add.
		//    (The `existingPathForAlias == path` case should ideally be caught by the `im.imports[path]` check
		//     before this loop, but this is a safeguard).
		if !isKeyword && (!aliasInUse || existingPathForAlias == path) {
			break
		}

		// If we are here, it means there's a conflict:
		// - The `finalAlias` is a keyword (even after initial adjustments, possibly due to prior `_pkg` suffixing if `aliasCandidate` itself was a keyword like `type_pkg` and then `type_pkg` is also a keyword in `goKeywords` - unlikely but defensive).
		// - Or, the `finalAlias` is already in use by a *different* package path.

		// Conflict: generate a new alias using the original `aliasCandidate` as base for numeric suffix.
		finalAlias = fmt.Sprintf("%s%d", aliasCandidate, counter) // Suffix to the original candidate
		counter++
		if counter > 100 { // Safety break for very unusual scenarios
			// Fallback to a more unique name based on a part of the path.
			// This is a very rough way to get a potentially more unique base.
			pathParts := strings.Split(path, "/")
			var uniquePart string
			if len(pathParts) > 1 {
				uniquePart = strings.ReplaceAll(pathParts[len(pathParts)-2], "-", "_")
				uniquePart = strings.ReplaceAll(uniquePart, ".", "_")
			} else {
				uniquePart = "p" // very generic prefix
			}
			baseHashAlias := uniquePart + "_" + filepath.Base(path) // e.g. github_com_my_pkg
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
				if altCounter > 20 { // Extremely unlikely deep conflict with fallback
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
		// Ensure the package is added and get its alias.
		// Pass the original package name (last part of path) as a requested alias hint.
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

// Imports returns a copy of the map of import paths to aliases for use in GoFile.
// It excludes entries where the alias is empty, as those typically don't need an explicit import line
// (e.g., the current package itself if it were ever added with an empty alias, though Add prevents this for currentPackagePath).
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
