// Package generator emits Go converters from parsed conversion rules.
//
// Pass a resolver, model.ParsedInfo and [Options] to [Generate]. Use
// [PackageIdents] to reserve existing names; [WarningsError] reports
// generation warnings when Options.Strict is set.
package generator
