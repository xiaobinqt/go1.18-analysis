// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sync

import (
	"sync/atomic"
	"unsafe"
)

// Map is like a Go map[interface{}]interface{} but is safe for concurrent use
// by multiple goroutines without additional locking or coordination.
// Loads, stores, and deletes run in amortized constant time.
//
// The Map type is specialized. Most code should use a plain Go map instead,
// with separate locking or coordination, for better type safety and to make it
// easier to maintain other invariants along with the map content.
//
// The Map type is optimized for two common use cases: (1) when the entry for a given
// key is only ever written once but read many times, as in caches that only grow,
// or (2) when multiple goroutines read, write, and overwrite entries for disjoint
// sets of keys. In these two cases, use of a Map may significantly reduce lock
// contention compared to a Go map paired with a separate Mutex or RWMutex.
//
// The zero Map is empty and ready for use. A Map must not be copied after first use.
type Map struct {
	mu Mutex

	// read contains the portion of the map's contents that are safe for
	// concurrent access (with or without mu held).
	//
	// The read field itself is always safe to load, but must only be stored with
	// mu held.
	//
	// Entries stored in read may be updated concurrently without mu, but updating
	// a previously-expunged entry requires that the entry be copied to the dirty
	// map and unexpunged with mu held.

	// ---------------------------------------
	// read map 由于是原子包托管，负责高性能，但是无法保证拥有全量的 key，因为对于新增 key
	// 会首先加到 dirty 中，所以 read 某种程度上，类似于一个 key 的快照，这个快照在某些情况下可能是全量快照。
	read atomic.Value // readOnly

	// dirty contains the portion of the map's contents that require mu to be
	// held. To ensure that the dirty map can be promoted to the read map quickly,
	// it also includes all of the non-expunged entries in the read map.
	//
	// Expunged entries are not stored in the dirty map. An expunged entry in the
	// clean map must be unexpunged and added to the dirty map before a new value
	// can be stored to it.
	//
	// If the dirty map is nil, the next write to the map will initialize it by
	// making a shallow copy of the clean map, omitting stale entries.

	// -------------------------------------
	// dirty map 拥有全量的 key，当 Store 操作要新增一个之前不存在的 key 的时候，预先是增加到 dirty 中的
	dirty map[any]*entry

	// misses counts the number of loads since the read map was last updated that
	// needed to lock mu to determine whether the key was present.
	//
	// Once enough misses have occurred to cover the cost of copying the dirty
	// map, the dirty map will be promoted to the read map (in the unamended
	// state) and the next store to the map will make a new dirty copy.

	// --------------------
	// 统计有多少次读取 read 没有命中。每次 read 中读取失败后，misses 的计数值都会加 1。
	misses int
}

// readOnly is an immutable struct stored atomically in the Map.read field.
type readOnly struct {
	m map[any]*entry
	// dirty 字典中有包含, read map 没有的 key
	amended bool // true if the dirty map contains some key not in m.
}

// expunged is an arbitrary pointer that marks entries which have been deleted
// from the dirty map.
// 如果为 expunged 表示从 dirty 中删除了
var expunged = unsafe.Pointer(new(any))

// An entry is a slot in the map corresponding to a particular key.
type entry struct {
	// p points to the interface{} value stored for the entry.
	//
	// If p == nil, the entry has been deleted, and either m.dirty == nil or
	// m.dirty[key] is e.
	//
	// If p == expunged, the entry has been deleted, m.dirty != nil, and the entry
	// is missing from m.dirty.
	//
	// Otherwise, the entry is valid and recorded in m.read.m[key] and, if m.dirty
	// != nil, in m.dirty[key].
	//
	// An entry can be deleted by atomic replacement with nil: when m.dirty is
	// next created, it will atomically replace nil with expunged and leave
	// m.dirty[key] unset.
	//
	// An entry's associated value can be updated by atomic replacement, provided
	// p != expunged. If p == expunged, an entry's associated value can be updated
	// only after first setting m.dirty[key] = e so that lookups using the dirty
	// map find the entry.
	p unsafe.Pointer // *interface{}
}

func newEntry(i any) *entry {
	return &entry{p: unsafe.Pointer(&i)}
}

// Load returns the value stored in the map for a key, or nil if no
// value is present.
// The ok result indicates whether value was found in the map.

// ---------------------------------------------
// 在查找指定的 key 的时候，总会先去只读字典中寻找，并不需要锁定互斥锁。
// 只有当 read 中没有，但 dirty 中可能会有这个 key 的时候，才会在锁的保护下去访问 dirty
func (m *Map) Load(key any) (value any, ok bool) {
	// 先读 read
	read, _ := m.read.Load().(readOnly)
	e, ok := read.m[key]
	// read 中不存在并且 dirty 中存在，在加锁的保护下去读取 dirty
	if !ok && read.amended {
		m.mu.Lock()
		// Avoid reporting a spurious miss if m.dirty got promoted while we were
		// blocked on m.mu. (If further loads of the same key will not miss, it's
		// not worth copying the dirty map for this key.)

		// 这里是一个双检查，!ok && read.amended 这条语句不是原子的，防止在操作是数据被修改
		read, _ = m.read.Load().(readOnly)
		e, ok = read.m[key]
		if !ok && read.amended {
			e, ok = m.dirty[key]
			// Regardless of whether the entry was present, record a miss: this key
			// will take the slow path until the dirty map is promoted to the read
			// map.
			m.missLocked()
		}
		m.mu.Unlock()
	}

	// dirty 中没有直接返回 nil
	if !ok {
		return nil, false
	}

	return e.load()
}

func (e *entry) load() (value any, ok bool) {
	p := atomic.LoadPointer(&e.p)

	println("ppp00", p, p == nil, p == expunged)

	if p == nil || p == expunged {
		return nil, false
	}

	println("ppp001 ", p, *(*any)(p) == nil)

	return *(*any)(p), true
}

// 保存一个 key,value 到 map 中
func (m *Map) Store(key, value any) {
	read, _ := m.read.Load().(readOnly)
	// 同时满足：read 中存在并且 e.p 不是 expunged
	if e, ok := read.m[key]; ok && e.tryStore(&value) {
		return
	}

	// 在不同时满足的情况下
	m.mu.Lock()
	// 虽然上面判断过，在正式操作的时候还需要再检查下，也就是双检查
	// 因为在判断之后有可能被其他协程修改数据
	read, _ = m.read.Load().(readOnly)
	// 如果 read 中存在, 那就是 e.p 是 expunged
	if e, ok := read.m[key]; ok {
		if e.unexpungeLocked() { // 将 e.p 从 expunged 转成 nil
			// The entry was previously expunged, which implies that there is a
			// non-nil dirty map and this entry is not in it.
			m.dirty[key] = e
		}
		e.storeLocked(&value) // 将 e.p 存入 value
	} else if e, ok := m.dirty[key]; ok { // dirty 中存在 key,更新 value,也就是更新 e.p
		e.storeLocked(&value)
	} else {
		// dirty 字段中没有这个 key,说明这个 key 是新的
		if !read.amended { // 在 dirty -> read 变换的时候 amended 会变成 false
			// We're adding the first new key to the dirty map.
			// Make sure it is allocated and mark the read-only map as incomplete.
			m.dirtyLocked()
			m.read.Store(readOnly{m: read.m, amended: true}) // 将 amended 改为 true
		}
		m.dirty[key] = newEntry(value) // 新值存到 dirty 中
	}
	m.mu.Unlock()
}

// tryStore stores a value if the entry has not been expunged.
//
// If the entry is expunged, tryStore returns false and leaves the entry
// unchanged.
func (e *entry) tryStore(i *any) bool {
	for {
		p := atomic.LoadPointer(&e.p)
		// 加载 p 的指针，如果 p == expunged 说明
		if p == expunged {
			return false
		}
		// 如果 e.p 的值不是 expunged，直接修改
		if atomic.CompareAndSwapPointer(&e.p, p, unsafe.Pointer(i)) {
			return true
		}
	}
}

// unexpungeLocked ensures that the entry is not marked as expunged.
//
// If the entry was previously expunged, it must be added to the dirty map
// before m.mu is unlocked.
// 将 expunged 转成 nil
func (e *entry) unexpungeLocked() (wasExpunged bool) {
	return atomic.CompareAndSwapPointer(&e.p, expunged, nil)
}

// storeLocked unconditionally stores a value to the entry.
//
// The entry must be known not to be expunged.
func (e *entry) storeLocked(i *any) {
	atomic.StorePointer(&e.p, unsafe.Pointer(i))
}

// LoadOrStore returns the existing value for the key if present.
// Otherwise, it stores and returns the given value.
// The loaded result is true if the value was loaded, false if stored.
func (m *Map) LoadOrStore(key, value any) (actual any, loaded bool) {
	// Avoid locking if it's a clean hit.
	read, _ := m.read.Load().(readOnly)
	if e, ok := read.m[key]; ok {
		actual, loaded, ok := e.tryLoadOrStore(value)
		if ok {
			return actual, loaded
		}
	}

	m.mu.Lock()
	read, _ = m.read.Load().(readOnly)
	if e, ok := read.m[key]; ok {
		if e.unexpungeLocked() {
			m.dirty[key] = e
		}
		actual, loaded, _ = e.tryLoadOrStore(value)
	} else if e, ok := m.dirty[key]; ok {
		actual, loaded, _ = e.tryLoadOrStore(value)
		m.missLocked()
	} else {
		if !read.amended {
			// We're adding the first new key to the dirty map.
			// Make sure it is allocated and mark the read-only map as incomplete.
			m.dirtyLocked()
			m.read.Store(readOnly{m: read.m, amended: true})
		}
		m.dirty[key] = newEntry(value)
		actual, loaded = value, false
	}
	m.mu.Unlock()

	return actual, loaded
}

// tryLoadOrStore atomically loads or stores a value if the entry is not
// expunged.
//
// If the entry is expunged, tryLoadOrStore leaves the entry unchanged and
// returns with ok==false.
func (e *entry) tryLoadOrStore(i any) (actual any, loaded, ok bool) {
	p := atomic.LoadPointer(&e.p)
	if p == expunged {
		return nil, false, false
	}
	if p != nil {
		return *(*any)(p), true, true
	}

	// Copy the interface after the first load to make this method more amenable
	// to escape analysis: if we hit the "load" path or the entry is expunged, we
	// shouldn't bother heap-allocating.
	ic := i
	for {
		if atomic.CompareAndSwapPointer(&e.p, nil, unsafe.Pointer(&ic)) {
			return i, false, true
		}
		p = atomic.LoadPointer(&e.p)
		if p == expunged {
			return nil, false, false
		}
		if p != nil {
			return *(*any)(p), true, true
		}
	}
}

// LoadAndDelete deletes the value for a key, returning the previous value if any.
// The loaded result reports whether the key was present.
func (m *Map) LoadAndDelete(key any) (value any, loaded bool) {
	read, _ := m.read.Load().(readOnly)
	e, ok := read.m[key]
	// 如果 read 中不存在这个 key 并且 dirty map 中存在这个 key
	if !ok && read.amended {
		m.mu.Lock()
		read, _ = m.read.Load().(readOnly)
		e, ok = read.m[key]
		if !ok && read.amended {
			e, ok = m.dirty[key]
			delete(m.dirty, key) // 直接删除 dirty 中的 key，不管 dirty 中存不存在
			// Regardless of whether the entry was present, record a miss: this key
			// will take the slow path until the dirty map is promoted to the read
			// map.
			m.missLocked()
		}
		m.mu.Unlock()
	}

	// read 中存在或是 dirty 中存在都会走到这里,执行 e.delete()
	if ok {
		return e.delete()
	}
	return nil, false
}

// Delete deletes the value for a key.
func (m *Map) Delete(key any) {
	m.LoadAndDelete(key)
}

// 把删除 key 的 value e.p 标记为 expunged
func (e *entry) delete() (value any, ok bool) {
	for {
		p := atomic.LoadPointer(&e.p)
		if p == nil || p == expunged {
			return nil, false
		}
		// 把 entry 中的 p 转成 nil，表示已经删除
		if atomic.CompareAndSwapPointer(&e.p, p, nil) {
			// *(*any)(p) 的作用是将指针 p 指向的数据转换为 any 类型，并且解引用该指针，以便访问 any 类型的值。
			return *(*any)(p), true
		}
	}
}

// Range calls f sequentially for each key and value present in the map.
// If f returns false, range stops the iteration.
//
// Range does not necessarily correspond to any consistent snapshot of the Map's
// contents: no key will be visited more than once, but if the value for any key
// is stored or deleted concurrently (including by f), Range may reflect any
// mapping for that key from any point during the Range call. Range does not
// block other methods on the receiver; even f itself may call any method on m.
//
// Range may be O(N) with the number of elements in the map even if f returns
// false after a constant number of calls.
func (m *Map) Range(f func(key, value any) bool) {
	// We need to be able to iterate over all of the keys that were already
	// present at the start of the call to Range.
	// If read.amended is false, then read.m satisfies that property without
	// requiring us to hold m.mu for a long time.
	read, _ := m.read.Load().(readOnly)
	if read.amended {
		// m.dirty contains keys not in read.m. Fortunately, Range is already O(N)
		// (assuming the caller does not break out early), so a call to Range
		// amortizes an entire copy of the map: we can promote the dirty copy
		// immediately!
		m.mu.Lock()
		read, _ = m.read.Load().(readOnly)
		if read.amended {
			read = readOnly{m: m.dirty}
			m.read.Store(read)
			m.dirty = nil
			m.misses = 0
		}
		m.mu.Unlock()
	}

	for k, e := range read.m {
		v, ok := e.load()
		if !ok {
			continue
		}
		if !f(k, v) {
			break
		}
	}
}

func (m *Map) missLocked() {
	m.misses++ // 不管在 dirty 中没有读到，miss 都执行 ++ 操作
	if m.misses < len(m.dirty) {
		return
	}

	println("dirty -> read 变换...", "misses 值为:", m.misses, "dirty 值为: ", len(m.dirty))

	// 如果 miss 的值 >= len(dirty)，将 dirty 赋给 read, dirty 和 miss 重新初始化
	// 此时 dirty 就为空了，说明从 dirty 全部过渡到了 read, 此时 read 是全量的
	m.read.Store(readOnly{m: m.dirty})
	m.dirty = nil
	m.misses = 0
}

func (m *Map) dirtyLocked() {
	if m.dirty != nil {
		return
	}

	read, _ := m.read.Load().(readOnly)
	m.dirty = make(map[any]*entry, len(read.m))

	println("read->dirty 转变")

	for k, e := range read.m {
		// 把 read 中 value e.p 不是 expunged 的 key,value 转到 dirty 中
		// 这里是循环，如果数据量大可能会非常耗时
		if !e.tryExpungeLocked() {
			m.dirty[k] = e
		}
	}
}

// 判断 e.p 是不是 expunged, 如果是 e.p 是 nil 则转为 expunged
func (e *entry) tryExpungeLocked() (isExpunged bool) {
	p := atomic.LoadPointer(&e.p)
	//println("444444444", p == nil)
	for p == nil {
		// 将 e.p 的 nil 转成 expunged
		if atomic.CompareAndSwapPointer(&e.p, nil, expunged) {
			return true
		}
		p = atomic.LoadPointer(&e.p)
	}
	return p == expunged
}
