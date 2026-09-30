package convutil

import (
	"context"
	"time"

	"github.com/podhmo/go-scan/examples/convert/model"
)

func TimeToString(ctx context.Context, ec *model.ErrorCollector, t time.Time) string {
	return t.Format(time.RFC3339)
}

func PtrTimeToString(ctx context.Context, ec *model.ErrorCollector, t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}
