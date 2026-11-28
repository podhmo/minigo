package storybook

import (
	"context"
	"errors"
	"fmt"
)

// Check renders every discovered story once and returns a joined error
// naming every failure. It is meant for host-side regression tests:
//
//	func TestStories(t *testing.T) {
//		if err := book.Check(t.Context()); err != nil {
//			t.Fatal(err)
//		}
//	}
//
// A story fails when [Book.Render] returns an error or when its response
// status is outside the 1xx–4xx range. A 4xx status is not a failure because
// handler stories can intentionally exercise not-found and validation-error
// pages; 5xx usually means the story or the rendering path broke. Check uses
// the same render path as [Book.Render], so host bindings registered by
// [Book.Setup] apply here too.
func (b *Book) Check(ctx context.Context) error {
	stories, err := b.Stories()
	if err != nil {
		return err
	}
	var errs []error
	for _, story := range stories {
		if err := ctx.Err(); err != nil {
			return err
		}
		page, err := b.Render(ctx, story.Name)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", story.Pos, err))
			continue
		}
		if page.Status < 100 || page.Status >= 500 {
			errs = append(errs, fmt.Errorf("%s: %s: unexpected status %d", story.Pos, story.Name, page.Status))
		}
	}
	return errors.Join(errs...)
}
