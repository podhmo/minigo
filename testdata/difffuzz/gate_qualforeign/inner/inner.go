package inner

import "time"

// Dur's initializer refers to the `time` package through THIS file's
// imports — a caller that never imports time must still see the
// declared-type tag.
var Dur = time.Second
