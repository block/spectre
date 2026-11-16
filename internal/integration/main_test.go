//go:build integration

package integration_test

import (
	"os"
	"testing"

	"github.com/block/spectre/internal/integration"
)

func TestMain(m *testing.M) {
	os.Exit(integration.Main(m, "sample-descriptors"))
}
