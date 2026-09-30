package goscan

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings" // Added
	"sync"
	// "time" // Removed: No longer needed after ModTime removal from fileMetadata
)

// fileMetadata stores metadata about a cached file.
type fileMetadata struct {
	// ModTime time.Time `json:"mod_time"` // Removed as per user feedback
	Symbols []string `json:"symbols"`
}

// cacheContent holds all data that is serialized to the cache file.
type cacheContent struct {
	Symbols map[string]string       `json:"symbols"` // Key: "<pkg_path>.<symbol_name>", Value: "relative_filepath"
	Files   map[string]fileMetadata `json:"files"`   // Key: "relative_filepath", Value: fileMetadata
}

// symbolCache manages the symbol definition cache.
// It is responsible for loading, saving, and providing access to cached symbol locations and file metadata.
type symbolCache struct {
	mu sync.RWMutex
	// cacheData map[string]string // Key: "<pkg_path>.<symbol_name>", Value: "filepath" // Replaced by content.Symbols
	// fileCacheData map[string]fileMetadata // Key: "relative_filepath", Value: fileMetadata // Replaced by content.Files
	content  cacheContent
	filePath string
	useCache bool
	rootDir  string // Project root directory, used to make filepaths relative
}

// newSymbolCache creates a new symbolCache.
//
// rootDir is the project's root directory. Filepaths in the cache will be relative to this directory.
// configCachePath is the user-configured path for the cache file. If empty, caching is disabled.
func newSymbolCache(rootDir string, configCachePath string) (*symbolCache, error) { // Removed configUseCache
	sc := &symbolCache{
		content: cacheContent{
			Symbols: make(map[string]string),
			Files:   make(map[string]fileMetadata),
		},
		rootDir: rootDir,
		// filePath and useCache will be set based on configCachePath
	}

	if configCachePath == "" {
		sc.useCache = false
		sc.filePath = "" // Ensure filePath is empty if no path is provided
		return sc, nil   // Caching is disabled, no further setup needed for path
	}

	// If configCachePath is provided, caching is considered enabled
	sc.useCache = true
	sc.filePath = configCachePath

	return sc, nil
}

// load loads the cache data from the file if useCache is true.
// If the file does not exist, it's not an error; an empty cache will be used.
func (sc *symbolCache) load(ctx context.Context) error {
	if !sc.useCache || sc.filePath == "" {
		return nil // Do nothing if cache is disabled or path is not set
	}

	sc.mu.Lock()
	defer sc.mu.Unlock()

	data, err := os.ReadFile(sc.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			sc.content = cacheContent{ // Initialize with empty maps
				Symbols: make(map[string]string),
				Files:   make(map[string]fileMetadata),
			}
			return nil // File not existing is not an error, just means no cache yet
		}
		return fmt.Errorf("failed to read cache file %s: %w", sc.filePath, err)
	}

	if len(data) == 0 { // Handle empty file case
		sc.content = cacheContent{ // Initialize with empty maps
			Symbols: make(map[string]string),
			Files:   make(map[string]fileMetadata),
		}
		return nil
	}

	var newContent cacheContent
	err = json.Unmarshal(data, &newContent)
	if err != nil {
		// If unmarshaling fails, treat it as a corrupted cache and start fresh
		slog.WarnContext(ctx, "Failed to unmarshal cache file, starting with an empty cache", slog.String("path", sc.filePath), slog.Any("error", err))
		sc.content = cacheContent{ // Initialize with empty maps
			Symbols: make(map[string]string),
			Files:   make(map[string]fileMetadata),
		}
		return nil // Return nil to allow the program to continue with an empty cache
	}
	sc.content = newContent
	// Ensure maps are not nil if JSON had them as null
	if sc.content.Symbols == nil {
		sc.content.Symbols = make(map[string]string)
	}
	if sc.content.Files == nil {
		sc.content.Files = make(map[string]fileMetadata)
	}
	return nil
}

// save saves the current cache data to the file if useCache is true.
func (sc *symbolCache) save() error {
	if !sc.useCache || sc.filePath == "" {
		return nil // Do nothing if cache is disabled or path is not set
	}

	sc.mu.RLock()
	// Create a deep copy of content to marshal, to avoid holding lock during MarshalIndent
	contentToSave := cacheContent{
		Symbols: make(map[string]string, len(sc.content.Symbols)),
		Files:   make(map[string]fileMetadata, len(sc.content.Files)),
	}
	for k, v := range sc.content.Symbols {
		contentToSave.Symbols[k] = v
	}
	for k, v := range sc.content.Files {
		contentToSave.Files[k] = v // fileMetadata is a struct, so direct assignment is a copy
	}
	sc.mu.RUnlock() // Unlock RLock before potential write operations (MarshalIndent, MkdirAll, WriteFile)

	// Marshal even if maps are empty to produce "{}" or {"symbols":{}, "files":{}}
	data, err := json.MarshalIndent(contentToSave, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal cache data: %w", err)
	}

	// Ensure the directory exists
	dir := filepath.Dir(sc.filePath)
	// Check if directory exists before trying to create it
	if _, statErr := os.Stat(dir); os.IsNotExist(statErr) {
		// MkdirAll also handles the case where the path already exists as a directory
		err = os.MkdirAll(dir, 0750) // Read/write/execute for user, read/execute for group
		if err != nil {
			return fmt.Errorf("failed to create cache directory %s: %w", dir, err)
		}
	} else if statErr != nil {
		// Some other error occurred when stating the directory
		return fmt.Errorf("failed to stat cache directory %s: %w", dir, statErr)
	}

	err = os.WriteFile(sc.filePath, data, 0640) // Read/write for user, read for group
	if err != nil {
		return fmt.Errorf("failed to write cache file %s: %w", sc.filePath, err)
	}
	return nil
}

// get retrieves a filepath for a given symbol key.
// The key is typically "<package_import_path>.<SymbolName>".
// Returns the filepath and true if found, otherwise an empty string and false.
// The returned filepath is an absolute path.
func (sc *symbolCache) get(key string) (string, bool) {
	if !sc.useCache {
		return "", false
	}

	sc.mu.RLock()
	defer sc.mu.RUnlock()

	relativePath, found := sc.content.Symbols[key]
	if !found {
		return "", false
	}
	// Cache stores relative paths, convert to absolute before returning
	if filepath.IsAbs(relativePath) { // Should not happen if saved correctly
		return relativePath, true // Should be logged as a warning or fixed upon load
	}
	return filepath.Join(sc.rootDir, relativePath), true
}

// setSymbol stores a filepath for a given symbol key.
// The key is typically "<package_import_path>.<SymbolName>".
// The filepath should be an absolute path; it will be converted to relative before storing.
func (sc *symbolCache) setSymbol(key string, absoluteFilepath string) error {
	if !sc.useCache {
		return nil
	}

	sc.mu.Lock()
	defer sc.mu.Unlock()

	if sc.rootDir == "" {
		return fmt.Errorf("rootDir is empty in symbolCache, cannot set symbol key %s. symbolCache must be initialized with a valid rootDir", key)
	}
	if !filepath.IsAbs(absoluteFilepath) {
		return fmt.Errorf("filepath to cache for symbol must be absolute, got %s for key %s", absoluteFilepath, key)
	}

	relativeFilepath, err := sc.makeRelative(absoluteFilepath)
	if err != nil {
		return fmt.Errorf("failed to make filepath relative for symbol key %s: %w", key, err)
	}

	sc.content.Symbols[key] = relativeFilepath
	// Also update the file's symbol list in fileCacheData
	// This assumes that when a symbol is set, its containing file's metadata should also be updated or created.
	// It might be better to have a separate mechanism for updating fileMetadata if symbols can be added without a full file rescan.
	// For now, let's ensure the file metadata knows about this symbol.
	// This part is tricky: setSymbol might be called without full fileMetadata (ModTime, all other symbols).
	// It's safer to manage fileMetadata.Symbols when fileMetadata is explicitly set via setFileMetadata.
	// So, we will *not* modify sc.content.Files[relativeFilepath].Symbols here directly.
	// Instead, the caller (e.g., goscan.Scanner) should ensure setFileMetadata is called after scanning.
	return nil
}

// setFileMetadata stores/updates the metadata for a given file.
// The absoluteFilepath will be converted to relative before storing.
func (sc *symbolCache) setFileMetadata(absoluteFilepath string, metadata fileMetadata) error {
	if !sc.useCache {
		return nil
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()

	if sc.rootDir == "" {
		return fmt.Errorf("rootDir is empty in symbolCache, cannot set file metadata for %s. symbolCache must be initialized with a valid rootDir", absoluteFilepath)
	}
	if !filepath.IsAbs(absoluteFilepath) {
		return fmt.Errorf("filepath for file metadata must be absolute, got %s", absoluteFilepath)
	}

	relativeFilepath, err := sc.makeRelative(absoluteFilepath)
	if err != nil {
		return fmt.Errorf("failed to make filepath relative for file metadata %s: %w", absoluteFilepath, err)
	}
	sc.content.Files[relativeFilepath] = metadata
	return nil
}

// removeSymbolsForFile removes all symbol entries from content.Symbols that point to the given relativeFilepath.
// This is a helper, typically called when a file is detected as deleted or its symbols are being refreshed.
// Must be called with appropriate lock if used externally (though primarily for internal use within other locked methods).
func (sc *symbolCache) removeSymbolsForFile(relativeFilepath string) {
	for key, path := range sc.content.Symbols {
		if path == relativeFilepath {
			delete(sc.content.Symbols, key)
		}
	}
}

// makeRelative converts an absolute path to a path relative to sc.rootDir.
// Assumes sc.rootDir is set and absoluteFilepath is absolute.
func (sc *symbolCache) makeRelative(absoluteFilepath string) (string, error) {
	cleanedRootDir := filepath.Clean(sc.rootDir)
	cleanedAbsFilepath := filepath.Clean(absoluteFilepath)

	rootDirPrefix := cleanedRootDir
	if cleanedRootDir != string(filepath.Separator) && !strings.HasSuffix(cleanedRootDir, string(filepath.Separator)) {
		rootDirPrefix += string(filepath.Separator)
	}

	if !strings.HasPrefix(cleanedAbsFilepath, rootDirPrefix) && cleanedAbsFilepath != cleanedRootDir {
		return "", fmt.Errorf("filepath %s is not within the configured rootDir %s", absoluteFilepath, sc.rootDir)
	}

	relativeFilepath, err := filepath.Rel(cleanedRootDir, cleanedAbsFilepath)
	if err != nil {
		return "", fmt.Errorf("filepath.Rel failed for %s relative to %s: %w", cleanedAbsFilepath, cleanedRootDir, err)
	}
	// Ensure all backslashes are converted to forward slashes before final ToSlash.
	// This handles cases where 'absoluteFilepath' might have contained '\' (e.g. from user input or cross-platform scenarios)
	// which, on Unix, might be part of a segment name after filepath.Clean and filepath.Rel.
	// We want to enforce '/' as the universal separator in the cache.
	universalRelativePath := strings.ReplaceAll(relativeFilepath, "\\", "/")
	return filepath.ToSlash(universalRelativePath), nil // filepath.ToSlash is good practice here, though universalRelativePath should already be fine.
}

// verifyAndGet checks if the symbol likely still exists at the cached path.
// It returns the absolute path if the file exists, otherwise it removes the
// stale entry from the cache and returns false.
// This is a basic check; it doesn't re-parse the file to confirm the symbol.
func (sc *symbolCache) verifyAndGet(ctx context.Context, key string) (string, bool) {
	if !sc.useCache {
		return "", false
	}

	sc.mu.RLock() // Start with RLock for initial checks
	relativePath, symbolFoundInSymbolsMap := sc.content.Symbols[key]
	rootDir := sc.rootDir // Read while RLock is held
	sc.mu.RUnlock()

	if !symbolFoundInSymbolsMap {
		return "", false
	}

	absolutePath := filepath.Join(rootDir, relativePath)
	if filepath.IsAbs(relativePath) { // Should not happen
		absolutePath = relativePath
	}

	// Check if the file still exists
	if _, err := os.Stat(absolutePath); err != nil {
		sc.mu.Lock()
		defer sc.mu.Unlock()

		// File doesn't exist or error stating it, remove the symbol from sc.content.Symbols
		delete(sc.content.Symbols, key)

		// Also, remove this specific symbol from the sc.content.Files[relativePath].Symbols list
		if fileMeta, ok := sc.content.Files[relativePath]; ok {
			newSymbols := []string{}
			symbolName := getSymbolNameFromKey(key) // Helper function needed
			for _, s := range fileMeta.Symbols {
				if s != symbolName {
					newSymbols = append(newSymbols, s)
				}
			}
			if len(newSymbols) < len(fileMeta.Symbols) { // If symbol was actually removed
				fileMeta.Symbols = newSymbols
				if len(fileMeta.Symbols) == 0 {
					// Optional: If no symbols left for this file, consider removing the file entry itself.
					// delete(sc.content.Files, relativePath)
					// For now, keep the file entry for its ModTime, unless it's also proven stale.
					// If we decide to delete, ensure this doesn't conflict with getFilesToScan logic.
				}
				sc.content.Files[relativePath] = fileMeta // Update the map with modified slice
			}
		}

		if !os.IsNotExist(err) {
			slog.WarnContext(ctx, "Removing cache entry due to error accessing file", slog.String("symbol_key", key), slog.String("file", absolutePath), slog.Any("error", err))
		}
		return "", false
	}

	return absolutePath, true
}

// getSymbolNameFromKey extracts the symbol name from a key like "pkg/path.SymbolName".
func getSymbolNameFromKey(key string) string {
	lastDot := strings.LastIndex(key, ".")
	if lastDot == -1 || lastDot == len(key)-1 {
		return key // Or handle error, though symbols usually have a package path
	}
	return key[lastDot+1:]
}

// getRootDir returns the project root directory used by the cache.
func (sc *symbolCache) getRootDir() string {
	return sc.rootDir
}

// getFilePath returns the path to the cache file.
func (sc *symbolCache) getFilePath() string {
	return sc.filePath
}

// isEnabled returns true if the cache is configured to be used.
func (sc *symbolCache) isEnabled() bool {
	return sc.useCache
}

// getFilesToScan analyzes the files in a given package directory against the cache.
// It identifies new files, existing (cached) files, and deleted files.
// It returns:
// - newFilesToScan: Absolute paths of new files in the directory not yet in cache.
// - existingFilesInCache: Absolute paths of files present in both directory and cache.
// - err: An error if any.
// It also cleans up cache entries for deleted files.
func (sc *symbolCache) getFilesToScan(ctx context.Context, packageDirPath string) (newFilesToScan []string, existingFilesInCache []string, err error) {
	if !sc.useCache {
		// If cache is disabled, the caller should ideally list all files and scan them.
		// This method's contract is to interact with the cache.
		return nil, nil, fmt.Errorf("cache is disabled, getFilesToScan should not be called in this state")
	}

	sc.mu.Lock() // Lock for the duration as we might modify cache
	defer sc.mu.Unlock()

	currentDirRelativeFiles := make(map[string]bool) // Relative paths (to rootDir) of files currently in the package directory

	dirEntries, err := os.ReadDir(packageDirPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read package directory %s: %w", packageDirPath, err)
	}

	for _, entry := range dirEntries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		// NOTE: We do not filter `_test.go` here. The cache should be a neutral
		// reflection of all .go files. The decision to include/exclude tests
		// belongs to the primary file-listing function (`listGoFiles`) that
		// receives the `includeTests` configuration.
		absPath := filepath.Join(packageDirPath, entry.Name())
		relPath, err := sc.makeRelative(absPath) // Converts to path relative to sc.rootDir
		if err != nil {
			slog.WarnContext(ctx, "Could not make path relative to root", slog.String("path", absPath), slog.String("root_dir", sc.rootDir), slog.Any("error", err))
			continue // Skip files not processable
		}
		currentDirRelativeFiles[relPath] = true

		// Check if this file (now identified by its path relative to rootDir) is in cache
		if _, isCached := sc.content.Files[relPath]; !isCached {
			newFilesToScan = append(newFilesToScan, absPath) // File is new to the cache
		} else {
			existingFilesInCache = append(existingFilesInCache, absPath) // File exists in directory and is in cache
		}
	}

	// Identify and clean up deleted files from cache
	// Iterate over cached files that are supposed to be in this packageDirPath
	cachedFilesInPackageScope := make([]string, 0)
	for relPathInCacheStore := range sc.content.Files {
		// To determine if relPathInCacheStore (relative to root) belongs to packageDirPath (absolute):
		// Construct its absolute path and check if it's within packageDirPath.
		absCachedFilePath := filepath.Join(sc.rootDir, relPathInCacheStore)

		// Check if the parent directory of absCachedFilePath is packageDirPath
		// A simple string prefix check on absolute paths is often sufficient if paths are clean.
		// Ensure packageDirPath is cleaned to avoid issues with trailing slashes.
		cleanedPackageDirPath := filepath.Clean(packageDirPath)
		if filepath.Dir(absCachedFilePath) == cleanedPackageDirPath {
			cachedFilesInPackageScope = append(cachedFilesInPackageScope, relPathInCacheStore)
		}
	}

	for _, relPathInCache := range cachedFilesInPackageScope {
		if _, stillExistsInDir := currentDirRelativeFiles[relPathInCache]; !stillExistsInDir {
			// This file, previously cached and belonging to this package, is no longer in the directory.
			delete(sc.content.Files, relPathInCache) // Remove from file metadata cache
			sc.removeSymbolsForFile(relPathInCache)  // Remove associated symbols from symbol map
		}
	}

	return newFilesToScan, existingFilesInCache, nil
}
