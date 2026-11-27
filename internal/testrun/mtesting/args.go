package mtesting

import (
	"fmt"
	"strings"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
)

// --- argument rendering ---

// argString renders one raw script value as text: untyped constants
// materialize, Named peels, cells deref.
func argString(v runtime.Value) string {
	return fmt.Sprint(hostbridge.GoNative(v))
}

// sprintArgs joins args like fmt.Sprintln (go's t.Log/Error bodies):
// operands separated by spaces, trailing newline trimmed by the caller.
func sprintArgs(args []runtime.Value) string {
	el := make([]any, len(args))
	for i, a := range args {
		el[i] = hostbridge.GoNative(a)
	}
	return strings.TrimSuffix(fmt.Sprintln(el...), "\n")
}

// sprintfArgs formats like t.Logf/Errorf: first arg the format string,
// rest the operands.
func sprintfArgs(args []runtime.Value) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("missing format argument")
	}
	format := argString(args[0])
	el := make([]any, len(args)-1)
	for i, a := range args[1:] {
		el[i] = hostbridge.GoNative(a)
	}
	return fmt.Sprintf(format, el...), nil
}

// checkCallable reports whether v can be vc.Call'd — a runtime func
// value or a builtin. t.Run/t.Cleanup reject anything else with a
// readable error instead of a deep VM failure.
func checkCallable(v runtime.Value) error {
	switch runtime.Unwrap(v).(type) {
	case *runtime.Function, *runtime.Closure, *runtime.BoundMethod, *runtime.BuiltinFunc:
		return nil
	}
	return fmt.Errorf("argument is not a function (%T)", v)
}

// outputWriter adapts t.Output() to io.Writer: writes decorate into
// the test's log buffer.
type outputWriter struct{ t *T }

func (w outputWriter) Write(b []byte) (int, error) {
	w.t.logMsg(strings.TrimSuffix(string(b), "\n"))
	return len(b), nil
}
