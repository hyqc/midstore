package midstore

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// ICache 本地缓存
type ICache[T any] interface {
	Add(data T)       //添加一条数据到本地缓存
	AddList(list []T) //添加一批数据到本地缓存
	Len() uint64      //本地缓存的长度
	Start()           //启动后台刷新携程
	Stop()            //停止后台刷新并释放资源
}

// IHandle 本地缓存回调
type IHandle[T any] interface {
	FlushCall(rows []T) error  //成功返回nil，失败返回错误
	FailedCall(rows []T) error //FlushCall执行失败时回调
}

// ILog 日志接口
type ILog interface {
	Debugf(format string, v ...any)
	Infof(format string, v ...any)
	Warnf(format string, v ...any)
	Errorf(format string, v ...any)
}

type Cache[T any] struct {
	rw           sync.RWMutex
	wg           sync.WaitGroup
	data         []T           //存储数据
	length       atomic.Uint64 //data的长度
	flushChannel chan struct{} //刷新信号
	ctx          context.Context
	cancel       context.CancelFunc
	h            IHandle[T]
	options      *Options
	ticker       *time.Ticker
	failedFile   *os.File //刷新失败后执行失败回调失败的数据直接写入本地文件系统
}

type Options struct {
	flushInterval     time.Duration //间隔多少时间向flushChannel发送一个执行信号
	maxLength         int           //最大缓存容量，Cache.length达到这个长度时向flushChannel发送一个执行信号
	log               ILog
	failedFileDir     string //失败落盘目录
	failedFileName    string //失败落盘文件名称
	enableLocalBackup bool   //是否启用失败后回调失败落盘
}

type Option func(*Options)

type StructType interface {
	MustStruct()
}

const (
	defaultMaxLength     int = 1000
	defaultFlushInterval     = time.Minute
)

func NewCache[T StructType](h IHandle[T], opts ...Option) *Cache[T] {
	ctx, cancel := context.WithCancel(context.Background())

	opt := &Options{
		maxLength:     defaultMaxLength,
		flushInterval: defaultFlushInterval,
		log:           newLog(),
	}

	for _, o := range opts {
		o(opt)
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
	}
}

func WithMaxLength(max int) Option {
	return func(o *Options) {
		if max <= 0 {
			max = defaultMaxLength
		}
		o.maxLength = max
	}
}

func WithFlushInterval(i time.Duration) Option {
	return func(o *Options) {
		if i <= 0 {
			i = defaultFlushInterval
		}
		o.flushInterval = i
	}
}

func WithLog(l ILog) Option {
	return func(o *Options) {
		if l == nil {
			l = newLog()
		}
		o.log = l
	}
}

func WithFailedFileDir(dir string) Option {
	return func(o *Options) {
		o.enableLocalBackup = dir != ""
		o.failedFileDir = dir
	}
}

// Add push data into Cache.data list front .
func (c *Cache[T]) Add(data T) {
	c.rw.Lock()
	defer c.rw.Unlock()
	c.data = append(c.data, data)
	c.sendFlushSignalIfReachMaxLength()
}

// AddList push data into Cache.data list front .
func (c *Cache[T]) AddList(elems []T) {
	c.rw.Lock()
	defer c.rw.Unlock()
	c.data = append(c.data, elems...)
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
	c.closeFailedFile()

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
		c.flushChannel <- struct{}{}
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
							c.options.log.Errorf("panic recovered: %v", err)
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
	c.options.log.Debugf("开始刷新数据")
	var err error
	//刷新数据
	if err = c.h.FlushCall(c.data); err == nil {
		c.options.log.Infof(fmt.Sprintf("FlushCall success list total: %v", total))
		return
	}
	c.options.log.Errorf(fmt.Sprintf("FlushCall error list total: %v, error: %v", total, err))
	if err = c.h.FailedCall(c.data); err == nil {
		c.options.log.Infof(fmt.Sprintf("FailedCall success list total: %v", total))
		return
	}
	c.options.log.Errorf(fmt.Sprintf("FailedCall error list total: %v, error: %v", total, err))
	c.failedCallBack(c.data)
}

func (c *Cache[T]) failedCallBack(rows []T) {
	if len(rows) == 0 {
		return
	}
	if _, err := c.getFailedFile(); err != nil {
		c.options.log.Errorf(fmt.Sprintf("getFailedFile error, data: %v, err: %v", rows, err))
		return
	}

	w := bufio.NewWriter(c.failedFile)
	for _, row := range rows {
		body, err := json.Marshal(row)
		if err != nil {
			c.options.log.Errorf("failedCallBack json.Marshal error,row: %+v：%v", row, err)
			continue
		}
		_, _ = w.Write(body)
		_, _ = w.WriteString("\n")
	}
	_ = w.Flush()

	return
}

func (c *Cache[T]) closeFailedFile() {
	if c.failedFile != nil {
		_ = c.failedFile.Close()
		c.failedFile = nil
	}
}

func (c *Cache[T]) openFailedFile(filename string) (*os.File, error) {
	return os.OpenFile(filename, os.O_CREATE|os.O_RDWR|os.O_APPEND, 06666)
}

func (c *Cache[T]) getFailedFile() (*os.File, error) {
	filename := c.getFailedFileName()
	if c.failedFile != nil && c.failedFile.Name() == filename {
		return c.failedFile, nil
	}

	if c.failedFile != nil {
		_ = c.failedFile.Close()
	}

	if _, err := os.Stat(c.options.failedFileDir); os.IsNotExist(err) {
		c.options.log.Infof("创建失败文件夹：%s", c.options.failedFileDir)
		os.MkdirAll(c.options.failedFileDir, 0755)
	}

	file, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	c.failedFile = file
	return file, nil
}

func (c *Cache[T]) getFailedFileName() string {
	return fmt.Sprintf("failed.%s.log", time.Now().Format("20060102"))
}
