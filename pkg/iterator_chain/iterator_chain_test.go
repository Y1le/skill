package iterator_chain

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIteratorCollect(t *testing.T) {
	assert := assert.New(t)

	iter := From([]int{1, 2, 3, 4, 5})
	collect := iter.Collect()
	assert.Equal([]int{1, 2, 3, 4, 5}, collect)
}

func TestIteratorForEach(t *testing.T) {
	assert := assert.New(t)

	iter := From([]int{1, 2, 3, 4, 5})
	cur := 1
	iter.ForEach(func(v int) {
		assert.Equal(v, cur)
		cur++
	})
}

func TestIteratorMap(t *testing.T) {
	assert := assert.New(t)
	iter := From([]int{1, 2, 3, 4, 5}).Map(func(v int) int {
		return v * 2
	})
	collect := iter.Collect()
	assert.Equal([]int{2, 4, 6, 8, 10}, collect)
}

func TestItratorFiler(t *testing.T) {
	assert := assert.New(t)
	iter := From([]int{1, 2, 3, 4, 5}).Filter(func(v int) bool {
		return v%2 == 0
	})
	collect := iter.Collect()
	assert.Equal([]int{2, 4}, collect)
}

func TestIteratorChain(t *testing.T) {
	assert := assert.New(t)
	res := From([]int{1, 2, 3, 4, 5}).Filter(func(v int) bool {
		return v%2 == 0
	}).Map(func(v int) int {
		return v * 2
	}).Collect()
	assert.Equal([]int{4, 8}, res)
}
