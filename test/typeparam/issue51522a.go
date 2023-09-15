// run -gcflags=-G=3

// Copyright 2022 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
package main


func f[T comparable](i any) {
	var t T

	if i != t {
		println("FAIL: if i != t")
	}
}

type myint int

func (m myint) foo() {
}

type fooer interface {
	foo()
}

type comparableFoo interface {
	comparable
	foo()
}

func g[T comparableFoo](i fooer) {
	var t T

	if i != t {
		println("FAIL: if i != t")
	}
}

func main() {
	f[int](int(0))
	g[myint](myint(0))
}
