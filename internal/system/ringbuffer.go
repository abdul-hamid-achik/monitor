package system

// RingBuffer is a fixed-size circular buffer that avoids memory allocations
type RingBuffer[T any] struct {
	data     []T
	capacity int
	head     int // insertion point
	size     int
}

// NewRingBuffer creates a new ring buffer with the specified capacity
func NewRingBuffer[T any](capacity int) *RingBuffer[T] {
	return &RingBuffer[T]{
		data:     make([]T, capacity),
		capacity: capacity,
		head:     0,
		size:     0,
	}
}

// Push adds a value to the buffer, overwriting the oldest if full
func (rb *RingBuffer[T]) Push(value T) {
	rb.data[rb.head] = value
	rb.head = (rb.head + 1) % rb.capacity
	if rb.size < rb.capacity {
		rb.size++
	}
}

// ToSlice returns the buffer contents as a slice in chronological order
func (rb *RingBuffer[T]) ToSlice() []T {
	if rb.size == 0 {
		return nil
	}

	result := make([]T, rb.size)
	if rb.size < rb.capacity {
		// Buffer not yet full, copy from 0 to head
		copy(result, rb.data[:rb.size])
	} else {
		// Buffer is full, copy from head to end, then from 0 to head
		copy(result, rb.data[rb.head:])
		copy(result[rb.capacity-rb.head:], rb.data[:rb.head])
	}
	return result
}

// Len returns the number of elements in the buffer
func (rb *RingBuffer[T]) Len() int {
	return rb.size
}

// Clear empties the buffer
func (rb *RingBuffer[T]) Clear() {
	rb.head = 0
	rb.size = 0
	var zero T
	for i := range rb.data {
		rb.data[i] = zero
	}
}

// Last returns the most recently added element
func (rb *RingBuffer[T]) Last() (T, bool) {
	if rb.size == 0 {
		var zero T
		return zero, false
	}
	idx := (rb.head - 1 + rb.capacity) % rb.capacity
	return rb.data[idx], true
}
