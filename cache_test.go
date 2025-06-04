package midstore

import (
	"fmt"
	"os"
	"os/signal"
	"testing"
	"time"
)

type elem struct {
	Id   int
	Name string
}

func (elem) MustStruct() {

}

type myHandle struct {
}

func (m *myHandle) FlushCall(rows []elem) error {
	for _, e := range rows {
		fmt.Println(e)
	}
	fmt.Println("刷新成功")
	return fmt.Errorf("失败1")
}

func (m *myHandle) FailedCall(rows []elem) error {
	for _, e := range rows {
		fmt.Println(e)
	}
	fmt.Println("失败回调成功")
	return fmt.Errorf("失败2")
}

func TestNewCache(t *testing.T) {
	myH := &myHandle{}
	c := NewCache(myH,
		WithMaxLength(20),
	)
	c.Start()

	ch := make(chan os.Signal, 1)

	go func() {
		i := 1
		for {
			c.Add(elem{
				Id:   i,
				Name: fmt.Sprintf("%v", i),
			})
			i++
			time.Sleep(time.Millisecond * 100)
			fmt.Println("长度：", c.Len())
		}
	}()

	signal.Notify(ch, os.Interrupt, os.Kill)

	select {
	case <-ch:
		c.Stop()
		fmt.Println("stop")
	}
}
