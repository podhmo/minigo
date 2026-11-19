package inner

import "time"

const N = 3

var Durations = make([]time.Duration, 1)
var Arrays = make([][N]uint16, 1)
var DurationCh = make(chan time.Duration, 1)
var ArrayCh = make(chan [N]uint16, 1)
var HostCh = time.After(0)

func ResetHostCh() {
	HostCh = time.After(0)
}
