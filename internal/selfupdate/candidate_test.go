package selfupdate

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type runnerFunc func(context.Context, string, []string, []string) ([]byte, []byte, error)

func (f runnerFunc) Run(ctx context.Context, path string, args, env []string) ([]byte, []byte, error) {
	return f(ctx, path, args, env)
}

func TestValidateCandidateRequiresExactVersionOutputAndScrubbedEnvironment(t *testing.T) {
	t.Setenv("TOKENDROP_API_KEY", "secret")
	v, _ := ParseVersion("0.3.0")
	runner := runnerFunc(func(_ context.Context, path string, args, env []string) ([]byte, []byte, error) {
		if path != "/candidate" || len(args) != 1 || args[0] != "version" {
			t.Fatalf("unexpected invocation %q %v", path, args)
		}
		for _, item := range env {
			if strings.HasPrefix(item, "TOKENDROP_") {
				t.Fatalf("participant environment reached candidate: %q", item)
			}
		}
		return []byte("dropin-miner 0.3.0\n"), nil, nil
	})
	if err := ValidateCandidate(context.Background(), runner, "/candidate", v); err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{"dropin-miner v0.3.0\n", "dropin-miner 0.3.1\n", "dropin-miner 0.3.0", "extra\ndropin-miner 0.3.0\n"} {
		err := ValidateCandidate(context.Background(), runnerFunc(func(context.Context, string, []string, []string) ([]byte, []byte, error) {
			return []byte(output), nil, nil
		}), "/candidate", v)
		if err == nil {
			t.Errorf("accepted candidate output %q", output)
		}
	}
}

func TestValidateCandidateReportsExecutionFailure(t *testing.T) {
	v, _ := ParseVersion("0.3.0")
	err := ValidateCandidate(context.Background(), runnerFunc(func(context.Context, string, []string, []string) ([]byte, []byte, error) {
		return nil, nil, errors.New("cannot execute")
	}), "/candidate", v)
	if err == nil || !strings.Contains(err.Error(), "cannot execute") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateCandidateTimeout(t *testing.T) {
	v, _ := ParseVersion("0.3.0")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := ValidateCandidate(ctx, runnerFunc(func(ctx context.Context, _ string, _ []string, _ []string) ([]byte, []byte, error) {
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}), "/candidate", v)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("timeout error = %v", err)
	}
}
