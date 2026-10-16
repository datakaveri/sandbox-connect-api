package main

import (
	"testing"
)

func TestNormalizeValue(t *testing.T) {
	tests := []struct {
		name     string
		input    float64
		expected float64
	}{
		// epsilon is 1e-10: only floating-point noise is zeroed. A small but
		// real per-minute charge (1e-6) must survive — rounding it away would
		// under-bill every short session.
		{
			name:     "floating-point noise below epsilon should be zero",
			input:    1e-12,
			expected: 0,
		},
		{
			name:     "value equal to epsilon should be zero",
			input:    1e-10,
			expected: 0,
		},
		{
			name:     "a small real charge should remain unchanged",
			input:    1e-6,
			expected: 1e-6,
		},
		{
			name:     "zero should remain zero",
			input:    0,
			expected: 0,
		},
		{
			name:     "large positive value should remain unchanged",
			input:    100.5,
			expected: 100.5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeValue(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeValue(%v) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}
