package main

import (
	"fmt"
	"sync"
)

func main() {
	var m = sync.Map{}

	m.Store("key", "11")
	fmt.Println(m.Load("key"))
}
