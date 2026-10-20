// Package boundmiss names a member a host-bound package does not
// have, so Resolve's miss trap has a field to chase (#921).
package boundmiss

import "time"

type T struct {
	At time.NoSuchMember
}
