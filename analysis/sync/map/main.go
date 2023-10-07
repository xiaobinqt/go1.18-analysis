package main

import (
	"fmt"
	"sync"
)

func main() {
	//var a any
	//a = 1
	//aa := &a
	//fmt.Println(aa)
	//
	//fmt.Println(unsafe.Pointer(&a))
	//fmt.Println(unsafe.Pointer(aa))
	//return

	//exit := make(chan os.Signal)
	var m = sync.Map{}

	m.Store("111", nil)
	fmt.Println(m.Load("111"))

	//m.Store("111", "11")
	//m.Store("2222", "11")
	//m.Store("333", "11")
	//
	//go func() {
	//	for {
	//		v, ok := m.Load("333")
	//		fmt.Println("Load(333)", v, ok)
	//		time.Sleep(1 * time.Second)
	//	}
	//}()
	//
	//m.Store("111", "4")
	//
	m.Delete("111")

	fmt.Println(m.Load("111"))

	//v, ok := m.LoadAndDelete("111")
	//fmt.Println("loadandDelete", v, ok)

	//for {
	//	select {
	//	case <-exit:
	//		return
	//	}
	//}

}
