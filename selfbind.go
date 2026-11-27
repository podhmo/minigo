package minigo

import (
	"go/ast"

	"github.com/podhmo/minigo/internal/stdlib"
	"github.com/podhmo/minigo/resolve"
	"github.com/podhmo/minigo/runtime"
)

// minigoSelfSymbols is the host table scripts receive when they import
// github.com/podhmo/minigo itself. Interpreting the engine's own source
// inside the engine cannot produce a working engine: the host-side
// construction invariants behind NewEngine are part of the embedding API.
// Methods are not listed here; host reflection dispatches them on the
// *Engine values NewEngine returns.
func minigoSelfSymbols(newEngine func(string, ...Option) *Engine) map[string]runtime.Value {
	namedBasic := func(name, underlying string, zero any) *runtime.TypeDef {
		td := &runtime.TypeDef{Name: "minigo." + name, Kind: runtime.KindNamedBasic, HostScalar: zero}
		if underlying != "" {
			td.Anon = ast.NewIdent(underlying)
		}
		return td
	}
	hostTypeAs := func(name string, x any) *runtime.TypeDef {
		td := HostTypeOf(x)
		td.Name = name
		return td
	}
	return map[string]runtime.Value{
		"Engine":         HostTypeOf(Engine{}),
		"Result":         HostTypeOf(Result{}),
		"ScriptProfiler": hostTypeAs("minigo.ScriptProfiler", ScriptProfiler{}),
		"VetResult":      hostTypeAs("minigo.VetResult", VetResult{}),
		"Option":         namedBasic("Option", "", Option(nil)),
		"BindOption":     namedBasic("BindOption", "", BindOption(nil)),
		"InitMode":       namedBasic("InitMode", "int", InitMode(0)),
		"PackageMode":    namedBasic("PackageMode", "int", PackageMode(0)),

		"GoCompatibleInit": GoCompatibleInit,
		"LazyInit":         LazyInit,
		"ModeAuto":         ModeAuto,
		"ModeSource":       ModeSource,
		"ModeDeny":         ModeDeny,

		"NewEngine":             stdlib.WrapBind("minigo.NewEngine", newEngine),
		"NewScriptProfiler":     stdlib.WrapBind("minigo.NewScriptProfiler", NewScriptProfiler),
		"WithBuildConfig":       stdlib.WrapBind("minigo.WithBuildConfig", WithBuildConfig),
		"WithInitMode":          stdlib.WrapBind("minigo.WithInitMode", WithInitMode),
		"WithPackageModes":      stdlib.WrapBind("minigo.WithPackageModes", WithPackageModes),
		"WithAllowedRoots":      stdlib.WrapBind("minigo.WithAllowedRoots", WithAllowedRoots),
		"WithHostPolicy":        stdlib.WrapBind("minigo.WithHostPolicy", WithHostPolicy),
		"WithOutput":            stdlib.WrapBind("minigo.WithOutput", WithOutput),
		"WithArgs":              stdlib.WrapBind("minigo.WithArgs", WithArgs),
		"WithStrictLazyCompile": stdlib.WrapBind("minigo.WithStrictLazyCompile", WithStrictLazyCompile),
		"WithScriptProfiler":    stdlib.WrapBind("minigo.WithScriptProfiler", WithScriptProfiler),
		"WithBytecodeVerify":    stdlib.WrapBind("minigo.WithBytecodeVerify", WithBytecodeVerify),
		"WithAssetAudit":        stdlib.WrapBind("minigo.WithAssetAudit", WithAssetAudit),
		"WithWorkingDir":        stdlib.WrapBind("minigo.WithWorkingDir", WithWorkingDir),
		"BindAs":                stdlib.WrapBind("minigo.BindAs", BindAs),
		"Format":                stdlib.WrapBind("minigo.Format", Format),
		"WrapFunc":              stdlib.WrapBind("minigo.WrapFunc", WrapFunc),
		"HostTypeOf":            stdlib.WrapBind("minigo.HostTypeOf", HostTypeOf),
		"ValueOf":               stdlib.WrapBind("minigo.ValueOf", ValueOf),
	}
}

// resolveSelfSymbols host-binds the public resolver API root-package
// options expose. BuildConfig especially must be a real host struct:
// minigo.WithBuildConfig consumes it by value across the host boundary.
func resolveSelfSymbols() map[string]runtime.Value {
	return map[string]runtime.Value{
		"BuildConfig":    HostTypeOf(resolve.BuildConfig{}),
		"PackageMeta":    HostTypeOf(resolve.PackageMeta{}),
		"RepoPackage":    HostTypeOf(resolve.RepoPackage{}),
		"GoScanResolver": HostTypeOf(resolve.GoScanResolver{}),
		"GoScanOption": &runtime.TypeDef{
			Name: "resolve.GoScanOption", Kind: runtime.KindNamedBasic,
			HostScalar: resolve.GoScanOption(nil),
		},
		"Resolver":         &runtime.TypeDef{Name: "resolve.Resolver", Kind: runtime.KindInterface, MReqs: []string{"Locate", "LocateDir"}},
		"FileListResolver": &runtime.TypeDef{Name: "resolve.FileListResolver", Kind: runtime.KindInterface, MReqs: []string{"LocateFiles"}},

		"FileImportPrefix":      resolve.FileImportPrefix,
		"ErrNoBuildableGoFiles": resolve.ErrNoBuildableGoFiles,
		"ErrUnresolved":         resolve.ErrUnresolved,
		"FileScopeFiles":        stdlib.WrapBind("resolve.FileScopeFiles", resolve.FileScopeFiles),
		"ReadPackageFiles":      stdlib.WrapBind("resolve.ReadPackageFiles", resolve.ReadPackageFiles),
		"ImportsC":              stdlib.WrapBind("resolve.ImportsC", resolve.ImportsC),
		"LooksLikeDir":          stdlib.WrapBind("resolve.LooksLikeDir", resolve.LooksLikeDir),
		"ModuleLang":            stdlib.WrapBind("resolve.ModuleLang", resolve.ModuleLang),
		"ModulePath":            stdlib.WrapBind("resolve.ModulePath", resolve.ModulePath),
		"RequiredVersion":       stdlib.WrapBind("resolve.RequiredVersion", resolve.RequiredVersion),
		"WithGOROOT":            stdlib.WrapBind("resolve.WithGOROOT", resolve.WithGOROOT),
		"NewGoScanResolver":     stdlib.WrapBind("resolve.NewGoScanResolver", resolve.NewGoScanResolver),
		"ScanRepo":              stdlib.WrapBind("resolve.ScanRepo", resolve.ScanRepo),
	}
}
