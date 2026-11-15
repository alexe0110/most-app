//go:build integration

package dbtest_test

import (
	"os"
	"testing"

	"github.com/alexe0110/most-app/internal/platform/db/dbtest"
)

// TestMain shares one Postgres container across the package's tests.
func TestMain(m *testing.M) { os.Exit(dbtest.Run(m)) }
