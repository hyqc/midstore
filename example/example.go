package main

import (
	"encoding/json"
	"fmt"
	"midstore"
	"os"
	"os/signal"
	"time"
)

type Elem struct {
	Id   int    `json:"id"`
	Name string `json:"name"`
}

func (e Elem) Marshal() ([]byte, error) {
	return json.Marshal(e)
}

type Handle struct{}

func NewHandle() *Handle {
	return &Handle{}
}

func (Handle) FlushCall(rows []Elem) error {
	for _, e := range rows {
		fmt.Println(e)
	}
	return fmt.Errorf("刷新失败")
}

func (Handle) FailedCall(rows []Elem) error {
	for _, e := range rows {
		body, _ := e.Marshal()
		fmt.Println(fmt.Sprintf("%+v", string(body)))
	}
	return fmt.Errorf("刷新失败")
}

func main() {
	client := midstore.NewCache(NewHandle(),
		midstore.WithMaxLength(20),
		midstore.WithFlushInterval(time.Minute*2),
		midstore.WithFailedFileDirAndMode(".", "test", 0755),
	)

	client.Start()

	ch := make(chan os.Signal, 1)

	go func() {
		i := 1
		for {
			client.Add(Elem{
				Id:   i,
				Name: fmt.Sprintf("%v", i),
			})
			fmt.Println(fmt.Sprintf("值：%v, 长度：%v", i, client.Len()))

			i++
			time.Sleep(time.Millisecond * 100)
		}
	}()

	signal.Notify(ch, os.Interrupt, os.Kill)

	select {
	case <-ch:
		client.Stop()
		fmt.Println("stop")
	}

}
