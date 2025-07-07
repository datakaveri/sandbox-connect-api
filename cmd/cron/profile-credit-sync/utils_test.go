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
		{
			name:     "value smaller than epsilon should be zero",
			input:    1e-6,
			expected: 0,
		},
		{
			name:     "value equal to epsilon should be zero",
			input:    1e-5,
			expected: 0,
		},
		{
			name:     "value larger than epsilon should remain unchanged",
			input:    1e-4,
			expected: 1e-4,
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
