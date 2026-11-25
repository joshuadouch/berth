package engine

import (
	"context"
	"fmt"
	"time"
)

// StartJob runs fn in the background and records its log. It returns the job ID.
func (e *Engine) StartJob(site, kind, title string, fn func(ctx context.Context, logf Logf) error) (int64, error) {
	account := ""
	if site != "" {
		if s, err := e.Store.Site(context.Background(), site); err == nil {
			account = s.AccountID
		}
	}
	id, err := e.Store.StartJob(context.Background(), site, account, kind, title)
	if err != nil {
		return 0, err
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		logf := func(format string, a ...any) {
			line := time.Now().Format("15:04:05") + "  " + fmt.Sprintf(format, a...)
			e.Store.AppendJobLog(ctx, id, line)
		}
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("internal error: %v", r)
				}
			}()
			err = fn(ctx, logf)
		}()
		if err != nil {
			logf("Failed: %v", err)
			e.Store.AddEvent(ctx, "error", kind, site, title+" failed: "+firstLine(err.Error()))
		} else {
			logf("Done")
		}
		e.Store.FinishJob(ctx, id, err)
	}()
	return id, nil
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}
