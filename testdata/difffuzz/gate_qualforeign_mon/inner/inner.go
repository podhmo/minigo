package inner

import "time"

// Mon takes the Named-member path (monthConst wraps January in a
// time.Month typedef tag) rather than the raw host-scalar path Dur
// takes — both must carry the member's type through the foreign chain.
var Mon = time.January
