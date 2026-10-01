package convutil

import (
	"context"
	"time"

	"github.com/podhmo/minigo/examples/convert-define/model"
)

func TimeToString(ctx context.Context, ec *model.ErrorCollector, t time.Time) string {
	return t.String()
}
