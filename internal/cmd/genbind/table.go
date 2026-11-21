package main

// spec is one generated bound package: Path is the stdlib import path,
// File the intrinsics_gen_<file>.go suffix, and Skip the member names
// that stay handwritten (custom adapters, richer typedefs, callback
// builtins) so the generated map never shadows them.
type spec struct {
	Path string
	File string
	Skip map[string]bool
}

var specs = []spec{
	{Path: "encoding/hex", File: "encoding_hex",
		// Encode/Decode write through the dst argument; the encoder
		// constructors take io.Writer/io.Reader — all handwritten.
		Skip: map[string]bool{"Encode": true, "Decode": true,
			"NewEncoder": true, "NewDecoder": true, "Dumper": true}},
	{Path: "encoding/base64", File: "encoding_base64",
		// the constructors adapt script io.Writer/io.Reader values.
		Skip: map[string]bool{"NewEncoder": true, "NewDecoder": true}},
	{Path: "crypto/sha256", File: "crypto_sha256"},
	{Path: "html", File: "html"},
	{Path: "path", File: "path"},
	{Path: "strconv", File: "strconv"},
	{Path: "unicode/utf8", File: "unicode_utf8",
		Skip: map[string]bool{"EncodeRune": true, "AppendRune": true}},
	{Path: "path/filepath", File: "path_filepath",
		Skip: map[string]bool{"Walk": true, "WalkDir": true,
			"Separator": true, "ListSeparator": true,
			"Abs": true, "Rel": true, "EvalSymlinks": true, "Glob": true}},
	{Path: "net/netip", File: "net_netip",
		Skip: map[string]bool{"Addr": true, "AddrPort": true, "Prefix": true}},
	{Path: "unicode", File: "unicode",
		// Upper/Lower/TitleCase are consts typed unicode.Case in source but
		// scripts spell them as ints — they stay handwritten.
		Skip: map[string]bool{"UpperCase": true, "LowerCase": true, "TitleCase": true}},
	{Path: "bytes", File: "bytes",
		// rune-predicate callbacks need the caller — a signature-pure
		// bind cannot adapt a script function (strings keeps its own
		// handwritten runePred adapters for the same reason).
		Skip: map[string]bool{"ContainsFunc": true, "FieldsFunc": true,
			"FieldsFuncSeq": true, "IndexFunc": true, "LastIndexFunc": true,
			"Map": true, "TrimFunc": true, "TrimLeftFunc": true,
			"TrimRightFunc": true}},
	{Path: "context", File: "context",
		Skip: map[string]bool{"Context": true, "CancelFunc": true, "CancelCauseFunc": true,
			"WithValue": true, "AfterFunc": true, "Cause": true,
			"WithCancel": true, "WithCancelCause": true,
			"WithDeadline": true, "WithDeadlineCause": true,
			"WithTimeout": true, "WithTimeoutCause": true, "WithoutCancel": true}},
}
