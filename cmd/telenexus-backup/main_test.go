package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunRejectsMissingOrUnknownCommands(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"unknown"},
		{"create"},
		{"verify"},
		{"drill"},
		{"restore"},
	} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if err := run(context.Background(), args, &stdout, &stderr); err == nil {
			t.Fatalf("run(%v) unexpectedly succeeded", args)
		}
	}
}

func TestRunCreateRequiresExplicitPaths(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := run(
		context.Background(),
		[]string{"create", "--source", "database.db"},
		&stdout,
		&stderr,
	)
	if err == nil || !strings.Contains(err.Error(), "--source and --output") {
		t.Fatalf("run(create) error = %v", err)
	}
}
