package collector

import "testing"

func TestRingBufferPush(t *testing.T) {
	rb := NewRingBuffer[int](5)
	rb.Push(1)
	rb.Push(2)
	rb.Push(3)
	got := rb.ToSlice()
	want := []int{1, 2, 3}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("slice[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestRingBufferWrap(t *testing.T) {
	rb := NewRingBuffer[int](3)
	for _, v := range []int{1, 2, 3, 4, 5} {
		rb.Push(v)
	}
	got := rb.ToSlice()
	want := []int{3, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("slice[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestRingBufferLastAndClear(t *testing.T) {
	rb := NewRingBuffer[int](3)
	if _, ok := rb.Last(); ok {
		t.Error("Last on empty should return false")
	}
	rb.Push(10)
	v, ok := rb.Last()
	if !ok || v != 10 {
		t.Errorf("Last = (%d, %t), want (10, true)", v, ok)
	}
	rb.Clear()
	if rb.Len() != 0 {
		t.Errorf("after Clear, Len = %d, want 0", rb.Len())
	}
}