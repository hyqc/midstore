package midstore

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultMaxLength     int = 1000        //本地容量，超过则触发落盘
	defaultFlushInterval     = time.Minute //本地落盘时间间隔，达到则触发落盘

	defaultFailedFileDir                 = "." // 落盘回调失败回调失败的备份数据目录
	defaultFailedFileDirMode os.FileMode = 0755
	defaultFailedFileName                = "failed" //失败落盘的文件开始名称
)

type Cache[T Type] struct {
	rw           sync.RWMutex
	wg           sync.WaitGroup
	data         []T           //存储数据
	length       atomic.Uint64 //data的长度
	flushChannel chan struct{} //刷新信号
	ctx          context.Context
	cancel       context.CancelFunc
	options      *Options
	ticker       *time.Ticker
	h            IHandle[T]
	writer       IWriter //刷新失败后执行失败回调失败的数据直接写入本地文件系统
	log          ILog
}

// FailedBackRows 回调失败的日志格式
type FailedBackRows[T Type] struct {
	Time string `json:"time"` //当前时间
	Data []T    `json:"data"` //保存失败的原始数据
}
type FailedBackRow[T Type] struct {
	Time string `json:"time"` //当前时间
	Data T      `json:"data"` //保存失败的原始数据
}

var _ ICache[Type] = &Cache[Type]{}

func NewCache[T Type](h IHandle[T], opts ...Option) *Cache[T] {
	ctx, cancel := context.WithCancel(context.Background())

	opt := &Options{
		maxLength:         defaultMaxLength,
		flushInterval:     defaultFlushInterval,
		failedFileDir:     defaultFailedFileDir,
		failedFileDirMode: defaultFailedFileDirMode,
		failedFileName:    defaultFailedFileName,
		log:               newLog(),
		enableLocalBackup: true,
	}

	for _, o := range opts {
		o(opt)
	}

	if opt.writer == nil {
		opt.writer = &defaultWriter{opt: opt}
	}

	defaultCap := opt.maxLength
	if defaultCap > 300 {
		// 5 * 60 每秒5条
		defaultCap = 300
	}

	return &Cache[T]{
		rw:           sync.RWMutex{},
		wg:           sync.WaitGroup{},
		length:       atomic.Uint64{},
		flushChannel: make(chan struct{}, 10),
		ctx:          ctx,
		cancel:       cancel,
		options:      opt,
		h:            h,
		data:         make([]T, 0, defaultCap),
		writer:       opt.writer,
		log:          opt.log,
	}
}

// Add push data into Cache.data list front .
func (c *Cache[T]) Add(row T) {
	c.rw.Lock()
	defer c.rw.Unlock()
	c.data = append(c.data, row)
	c.sendFlushSignalIfReachMaxLength()
}

// AddList push data into Cache.data list front .
func (c *Cache[T]) AddList(rows []T) {
	if len(rows) == 0 {
		return
	}

	c.rw.Lock()
	defer c.rw.Unlock()
	c.data = append(c.data, rows...)
	c.sendFlushSignalIfReachMaxLength()
}

// Len returns the Cache.data element length .
func (c *Cache[T]) Len() int {
	c.rw.RLock()
	defer c.rw.RUnlock()
	return len(c.data)
}

func (c *Cache[T]) Start() {
	go c.run()
	go c.tick()
}

func (c *Cache[T]) Stop() {
	if c.ticker != nil {
		c.ticker.Stop()
	}

	c.wg.Add(1)
	c.cancel()
	c.wg.Wait()
	_ = c.writer.Close()

	if c.flushChannel != nil {
		close(c.flushChannel)
	}

}

func (c *Cache[T]) sendFlushSignalIfReachMaxLength() {
	if len(c.data) >= c.options.maxLength {
		c.flushChannel <- struct{}{}
	}
}

func (c *Cache[T]) tick() {
	if c.options.flushInterval <= 0 {
		c.options.flushInterval = time.Minute
	}
	c.ticker = time.NewTicker(c.options.flushInterval)
	for range c.ticker.C {
		select {
		case c.flushChannel <- struct{}{}:
		case <-c.ctx.Done():
			return
		}
	}
}

func (c *Cache[T]) run() {
	for {
		select {
		case _, ok := <-c.flushChannel:
			if ok && c.h != nil {
				func() {
					defer func() {
						if err := recover(); err != nil {
							c.log.Errorf("panic recovered: %v", err)
						}
					}()
					c.flush()
				}()
			}
		case <-c.ctx.Done():
			if c.h != nil {
				c.flush()
				c.wg.Done()
			}
			return
		}
	}
}

func (c *Cache[T]) flush() {
	c.rw.Lock()
	defer c.rw.Unlock()
	if c.h == nil {
		return
	}

	total := len(c.data)
	if total == 0 {
		return
	}

	defer func() {
		c.data = c.data[:0]
	}()

	c.log.Debugf("开始刷新数据，共 %d 条", total)

	var err error
	//刷新数据
	if err = c.h.FlushCall(c.data); err == nil {
		c.log.Infof("FlushCall success list total: %d", total)
		return
	} else {
		c.log.Errorf("FlushCall error list total: %d, error: %v", total, err)
	}

	if err = c.h.FailedCall(c.data); err == nil {
		c.log.Infof("FailedCall success list total: %d", total)
		return
	} else {
		c.log.Errorf("FailedCall error list total: %d, error: %v", total, err)
	}

	c.failedCallBack(c.data)
}

func (c *Cache[T]) failedCallBack(rows []T) {
	if !c.options.enableLocalBackup || len(rows) == 0 {
		return
	}
	file, err := c.writer.GetWriter()
	if err != nil {
		c.log.Errorf("getFailedFile error, data: %v, err: %v", rows, err)
		return
	}

	w := bufio.NewWriter(file)
	if c.options.failedBackRows {
		c.saveBackRows(w, rows)
	} else {
		c.saveBackRow(w, rows)
	}

	return
}

func (c *Cache[T]) saveBackRows(w *bufio.Writer, rows []T) {
	backData := FailedBackRows[T]{
		Time: time.Now().Format(time.RFC3339),
		Data: rows,
	}
	body, _ := json.Marshal(backData)
	strBody := string(body)
	if _, err := w.Write(body); err != nil {
		c.log.Errorf("failedCallBack w.Write body error,body: %s, err: %v", strBody, err)
		return
	}
	_, _ = w.Write([]byte("\n"))

	if err := w.Flush(); err != nil {
		c.log.Errorf("failedCallBack w.Flush error,body: %v：%v", strBody, err)
	}
}

func (c *Cache[T]) saveBackRow(w *bufio.Writer, rows []T) {
	now := time.Now().Format(time.RFC3339)
	for _, row := range rows {
		item := FailedBackRow[T]{
			Time: now,
			Data: row,
		}
		body, _ := json.Marshal(item)
		if _, err := w.Write(body); err != nil {
			c.log.Errorf("failedCallBack w.Write body error,body: %s, err: %v", string(body), err)
			continue
		}
		_, _ = w.Write([]byte("\n"))
	}

	if err := w.Flush(); err != nil {
		c.log.Errorf("failedCallBack w.Flush error,rows: %v：%v", rows, err)
	}
}
