package lru

type GroupLRU[K1, K2 comparable, V any] struct {
	list  *list[K1]
	items map[K1]cacheItem[K1, map[K2]V]
	count int
	cap   int
}

func NewGroupLRU[K1, K2 comparable, V any](capacity int) GroupLRU[K1, K2, V] {
	if capacity <= 0 {
		capacity = 1
	}
	c := GroupLRU[K1, K2, V]{
		items: make(map[K1]cacheItem[K1, map[K2]V]),
		list:  newList[K1](),
		count: 0,
		cap:   capacity,
	}
	return c
}

func (c *GroupLRU[K1, K2, V]) Add(key1 K1, key2 K2, value V) {
	if innerItems, ok1 := c.items[key1]; ok1 {
		if _, ok2 := innerItems.value[key2]; ok2 {
			innerItems.value[key2] = value
			c.items[key1] = innerItems
			c.list.moveToFront(innerItems.elem)
			return
		}
	}

	var elem *listElem[K1]
	for c.count >= c.cap {
		elem = c.list.removeLast()
		ek := elem.v
		ev := c.items[ek]
		delete(c.items, ek)
		c.count -= len(ev.value)
	}

	if innerItems, ok := c.items[key1]; ok {
		innerItems.value[key2] = value
		c.items[key1] = innerItems
		c.list.moveToFront(innerItems.elem)
		c.count++
		return
	}

	if elem == nil {
		elem = new(listElem[K1])
	}
	elem.v = key1
	innerItems := cacheItem[K1, map[K2]V]{elem, make(map[K2]V)}
	innerItems.value[key2] = value
	c.items[key1] = innerItems
	c.list.pushElem(elem)
	c.count++
}

func (c *GroupLRU[K1, K2, V]) Get(key1 K1, key2 K2) (value V, ok bool) {
	innerItems, ok := c.items[key1]
	if !ok {
		return value, false
	}
	item, ok := innerItems.value[key2]
	if !ok {
		return value, false
	}
	c.list.moveToFront(innerItems.elem)
	return item, true
}

func (c *GroupLRU[K1, K2, V]) Purge() {
	c.list.init()
	clear(c.items)
	c.count = 0
}

func (c *GroupLRU[K1, K2, V]) Remove(key1 K1, key2 K2) {
	innerItems, ok := c.items[key1]
	if !ok {
		return
	}
	if _, ok = innerItems.value[key2]; !ok {
		return
	}
	delete(innerItems.value, key2)
	if len(innerItems.value) != 0 {
		c.items[key1] = innerItems
	} else {
		delete(c.items, key1)
		c.list.remove(innerItems.elem)
	}
	c.count--
}

func (c *GroupLRU[K1, K2, V]) RemoveGroup(key1 K1) {
	innerItems, ok := c.items[key1]
	if !ok {
		return
	}
	delete(c.items, key1)
	c.list.remove(innerItems.elem)
	c.count -= len(innerItems.value)
}
