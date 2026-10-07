package expr

import (
	"context"
)

// StatusSnapshot evaluates status predicates over the caller's dependency snapshot.
// Dependencies must contain all jobs relevant to the condition's dependency graph.
type StatusSnapshot struct {
	Scope        string
	Canceled     bool
	JobStatus    string
	Dependencies *Object
}

// Status implements StatusProvider, following the documented step/job distinction.
func (s StatusSnapshot) Status(ctx context.Context, name string, names []string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if name == "always" {
		if len(names) > 0 {
			return false, expressionError("TRL-EXPR-0001", Span{}, "always takes no job names")
		}
		return true, nil
	}
	if name == "canceled" {
		if len(names) > 0 {
			return false, expressionError("TRL-EXPR-0001", Span{}, "canceled takes no job names")
		}
		return s.Canceled, nil
	}
	if name != "failed" && name != "succeeded" && name != "succeededorfailed" {
		return false, expressionError("TRL-EXPR-0001", Span{}, "unknown status function")
	}
	if s.Scope == "step" {
		if len(names) > 0 {
			return false, expressionError("TRL-EXPR-0001", Span{}, "job names unavailable in step conditions")
		}
		return statusMatches(name, s.JobStatus), nil
	}
	if s.Canceled && name != "failed" {
		return false, nil
	}
	jobs := []Value{}
	if len(names) > 0 {
		for _, job := range names {
			v, ok := s.Dependencies.Lookup(job)
			if !ok {
				return false, expressionError("TRL-EXPR-0001", Span{}, "unknown dependency")
			}
			jobs = append(jobs, v)
		}
	} else if s.Dependencies != nil {
		jobs, _ = valuesOf(s.Dependencies)
	}
	for _, job := range jobs {
		result, _ := lookupProperty(job, "result")
		text, _ := stringify(result)
		matched := statusMatches(name, text)
		if name == "failed" && matched {
			return true, nil
		}
		if name != "failed" && !matched {
			return false, nil
		}
	}
	return name != "failed", nil
}

func statusMatches(name, result string) bool {
	failed := ordinalFold(result) == "FAILED"
	succeeded := ordinalFold(result) == "SUCCEEDED" || ordinalFold(result) == "SUCCEEDEDWITHISSUES"
	switch name {
	case "failed":
		return failed
	case "succeeded":
		return succeeded
	case "succeededorfailed":
		return failed || succeeded
	default:
		return false
	}
}
