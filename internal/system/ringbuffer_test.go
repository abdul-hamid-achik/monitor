package system

import (
	"testing"
)

func TestRingBuffer_Push(t *testing.T) {
	rb := NewRingBuffer[int](5)

	// Test push and get values
	rb.Push(1)
	rb.Push(2)
	rb.Push(3)

	slice := rb.ToSlice()
	if len(slice) != 3 {
		t.Errorf("Expected length 3, got %d", len(slice))
	}
	if slice[0] != 1 || slice[1] != 2 || slice[2] != 3 {
		t.Errorf("Expected [1, 2, 3], got %v", slice)
	}
}

func TestRingBuffer_CircularOverwrite(t *testing.T) {
	rb := NewRingBuffer[int](3)

	// Fill buffer
	rb.Push(1)
	rb.Push(2)
	rb.Push(3)

	// Overwrite oldest
	rb.Push(4)

	slice := rb.ToSlice()
	if len(slice) != 3 {
		t.Errorf("Expected length 3, got %d", len(slice))
	}
	// Should have [2, 3, 4] now
	if slice[0] != 2 || slice[1] != 3 || slice[2] != 4 {
		t.Errorf("Expected [2, 3, 4], got %v", slice)
	}
}

func TestRingBuffer_Empty(t *testing.T) {
	rb := NewRingBuffer[int](5)

	slice := rb.ToSlice()
	if slice != nil {
		t.Errorf("Expected nil for empty buffer, got %v", slice)
	}

	_, ok := rb.Last()
	if ok {
		t.Error("Expected Last() to return false for empty buffer")
	}
}

func TestRingBuffer_Last(t *testing.T) {
	rb := NewRingBuffer[int](5)

	rb.Push(10)
	val, ok := rb.Last()
	if !ok || val != 10 {
		t.Errorf("Expected Last() = (10, true), got (%d, %t)", val, ok)
	}

	rb.Push(20)
	val, ok = rb.Last()
	if !ok || val != 20 {
		t.Errorf("Expected Last() = (20, true), got (%d, %t)", val, ok)
	}
}

func TestRingBuffer_Clear(t *testing.T) {
	rb := NewRingBuffer[int](5)

	rb.Push(1)
	rb.Push(2)
	rb.Push(3)

	rb.Clear()

	if rb.Len() != 0 {
		t.Errorf("Expected length 0 after Clear(), got %d", rb.Len())
	}

	slice := rb.ToSlice()
	if slice != nil {
		t.Errorf("Expected nil after Clear(), got %v", slice)
	}
}

func TestRingBuffer_Float64(t *testing.T) {
	rb := NewRingBuffer[float64](5)

	rb.Push(1.5)
	rb.Push(2.5)
	rb.Push(3.5)

	slice := rb.ToSlice()
	if len(slice) != 3 {
		t.Errorf("Expected length 3, got %d", len(slice))
	}
	if slice[0] != 1.5 || slice[1] != 2.5 || slice[2] != 3.5 {
		t.Errorf("Expected [1.5, 2.5, 3.5], got %v", slice)
	}
}
