package main

import "testing"

func TestValidPassword(t *testing.T) {
	tests := []struct {
		name     string
		password string
		valid    bool
	}{
		{name: "eight characters", password: "12345678", valid: true},
		{name: "unicode passphrase", password: "西湖断桥安全口令", valid: true},
		{name: "too short", password: "1234567", valid: false},
		{name: "control character", password: "valid123\n", valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validPassword(test.password); got != test.valid {
				t.Fatalf("validPassword() = %v, want %v", got, test.valid)
			}
		})
	}
}
