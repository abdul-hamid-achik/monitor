package collector

// RingBuffer is a fixed-size circular buffer that avoids allocations.
type RingBuffer[T any] struct {
	data     []T
	capacity int
	head     int
	size     int
}

// NewRingBuffer creates a new ring buffer with the given capacity.
func NewRingBuffer[T any](capacity int) *RingBuffer[T] {
	return &RingBuffer[T]{
		data:     make([]T, capacity),
		capacity: capacity,
	}
}

// Push adds a value, overwriting the oldest when full.
func (rb *RingBuffer[T]) Push(value T) {
	rb.data[rb.head] = value
	rb.head = (rb.head + 1) % rb.capacity
	if rb.size < rb.capacity {
		rb.size++
	}
}

// ToSlice returns contents in chronological order.
func (rb *RingBuffer[T]) ToSlice() []T {
	if rb.size == 0 {
		return nil
	}
	out := make([]T, rb.size)
	if rb.size < rb.capacity {
		copy(out, rb.data[:rb.size])
	} else {
		copy(out, rb.data[rb.head:])
		copy(out[rb.capacity-rb.head:], rb.data[:rb.head])
	}
	return out
}

// Len returns the number of elements stored.
func (rb *RingBuffer[T]) Len() int { return rb.size }

// Clear empties the buffer.
func (rb *RingBuffer[T]) Clear() {
	rb.head = 0
	rb.size = 0
	var zero T
	for i := range rb.data {
		rb.data[i] = zero
	}
}

// Last returns the most recently pushed element and true, or zero and false if empty.
func (rb *RingBuffer[T]) Last() (T, bool) {
	if rb.size == 0 {
		var zero T
		return zero, false
	}
	idx := (rb.head - 1 + rb.capacity) % rb.capacity
	return rb.data[idx], true
}