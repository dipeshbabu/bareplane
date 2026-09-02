package doctor

import (
	"context"
)

type Status string

const (
	StatusPass Status = "pass"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
)

type Result struct {
	Name    string
	Status  Status
	Message string
}

type Check interface {
	Run(context.Context) Result
}

type CheckFunc func(context.Context) Result

func (f CheckFunc) Run(ctx context.Context) Result {
	return f(ctx)
}

type Report struct {
	Results []Result
}

func Run(ctx context.Context, checks []Check) Report {
	results := make([]Result, 0, len(checks))
	for _, check := range checks {
		if err := ctx.Err(); err != nil {
			results = append(results, Result{
				Name:    "context",
				Status:  StatusFail,
				Message: err.Error(),
			})
			break
		}
		results = append(results, check.Run(ctx))
	}
	return Report{Results: results}
}

func (r Report) HasFailures() bool {
	for _, result := range r.Results {
		if result.Status == StatusFail {
			return true
		}
	}
	return false
}
