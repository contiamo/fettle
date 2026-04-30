package agent

import (
	"context"
	"strings"
	"testing"
)

func TestRun_unknownAgent(t *testing.T) {
	_, err := Run(context.Background(), Spec{Name: "nonsense"}, "hi")
	if err == nil || !strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("want unknown-agent error, got %v", err)
	}
}
