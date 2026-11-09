package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDriveName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Паспорт", "Паспорт"},
		{"a/b", "a_b"},
		{`a\b/c`, "a_b_c"},
		{"../etc/passwd", ".._etc_passwd"},
		{"", ""},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, driveName(tt.in), tt.in)
	}
}
