package mtesting

import (
	"strconv"
	"time"

	"github.com/podhmo/minigo/internal/hostbridge"
	"github.com/podhmo/minigo/runtime"
)

func parseStringFlag(s string) (runtime.Value, error) { return s, nil }

func parseBoolFlag(s string) (runtime.Value, error) {
	v, err := strconv.ParseBool(s)
	if err != nil {
		return nil, err
	}
	return v, nil
}

func parseIntFlag(s string) (runtime.Value, error) {
	v, err := strconv.ParseInt(s, 0, 64)
	if err != nil {
		return nil, err
	}
	return v, nil
}

func parseUintFlag(s string) (runtime.Value, error) {
	v, err := strconv.ParseUint(s, 0, 64)
	if err != nil {
		return nil, err
	}
	return int64(v), nil
}

func parseFloatFlag(s string) (runtime.Value, error) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, err
	}
	return v, nil
}

func parseDurationFlag(s string) (runtime.Value, error) {
	v, err := time.ParseDuration(s)
	if err != nil {
		return nil, err
	}
	return v, nil
}

func intArg(v runtime.Value) int64 {
	switch x := hostbridge.GoNative(v).(type) {
	case int64:
		return x
	case int:
		return int64(x)
	}
	return 0
}

func floatArg(v runtime.Value) float64 {
	switch x := hostbridge.GoNative(v).(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	}
	return 0
}
