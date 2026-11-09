package randomx

import (
	"encoding/hex"
	"testing"
)

func TestOfficialRandomXVectorsLightMode(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		input    string
		expected string
	}{
		{
			name:     "Vector A (Light Mode)",
			key:      "test key 000",
			input:    "This is a test",
			expected: "639183aae1bf4c9a35884cb46b09cad9175f04efd7684e7262a0ac1c2f0b4e3f",
		},
		{
			name:     "Vector B (Light Mode)",
			key:      "test key 000",
			input:    "Lorem ipsum dolor sit amet",
			expected: "300a0adb47603dedb42228ccb2b211104f4da45af709cd7547cd049e9489c969",
		},
		{
			name:     "Vector C (Light Mode)",
			key:      "test key 000",
			input:    "sed do eiusmod tempor incididunt ut labore et dolore magna aliqua",
			expected: "c36d4ed4191e617309867ed66a443be4075014e2b061bcdaf9ce7b721d2b77a8",
		},
		{
			name:     "Vector D (Light Mode - New Key)",
			key:      "test key 001",
			input:    "sed do eiusmod tempor incididunt ut labore et dolore magna aliqua",
			expected: "e9ff4503201c0c2cca26d285c93ae883f9b1d30c9eb240b820756f2d5a7905fc",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, err := NewRxContext([]byte(tc.key), false) // false = Light Mode
			if err != nil {
				t.Fatalf("Failed to create RxContext: %v", err)
			}
			defer ctx.Close()

			hash, err := ctx.CalculateHash([]byte(tc.input))
			if err != nil {
				t.Fatalf("CalculateHash failed: %v", err)
			}

			actualHex := hex.EncodeToString(hash[:])
			if actualHex != tc.expected {
				t.Errorf("Hash mismatch:\nExpected: %s\nActual:   %s", tc.expected, actualHex)
			}
		})
	}
}

func TestConcurrentHashing(t *testing.T) {
	ctx, err := NewRxContext([]byte("test key 000"), false)
	if err != nil {
		t.Fatalf("Failed to create RxContext: %v", err)
	}
	defer ctx.Close()

	expectedHex := "639183aae1bf4c9a35884cb46b09cad9175f04efd7684e7262a0ac1c2f0b4e3f"
	input := []byte("This is a test")

	const goroutines = 4
	errChan := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			hash, err := ctx.CalculateHash(input)
			if err != nil {
				errChan <- err
				return
			}
			if hex.EncodeToString(hash[:]) != expectedHex {
				errChan <- err
				return
			}
			errChan <- nil
		}()
	}

	for i := 0; i < goroutines; i++ {
		if err := <-errChan; err != nil {
			t.Fatalf("Concurrent hashing failed: %v", err)
		}
	}
}

func TestConcurrentHashingAndCloseRace(t *testing.T) {
	ctx, err := NewRxContext([]byte("test key race 001"), false)
	if err != nil {
		t.Fatalf("Failed to create RxContext: %v", err)
	}

	const workers = 4
	done := make(chan struct{})
	for i := 0; i < workers; i++ {
		go func(id int) {
			for j := 0; j < 8; j++ {
				_, _ = ctx.CalculateHash([]byte("concurrent hash input under close"))
			}
			done <- struct{}{}
		}(i)
	}

	// Trigger Close concurrently while hashing goroutines are in flight
	ctx.Close()

	for i := 0; i < workers; i++ {
		<-done
	}
}

