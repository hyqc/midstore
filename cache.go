package midstore

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

type Type interface {
	Marshal() ([]byte, error)
}

// ICache 本地缓存
type ICache[T Type] interface {
	Add(row T)        //添加一条数据到本地缓存
	AddList(rows []T) //添加一批数据到本地缓存
	Len() int         //本地缓存的长度
	Start()           //启动后台刷新携程
	Stop()            //停止后台刷新并释放资源
}

// IHandle 本地缓存回调
type IHandle[T Type] interface {
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

// IWriter 落盘策略
type IWriter interface {
	GetWriter() (*os.File, error)
	Close() error
}

type Cache[T Type] struct {
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
	writer       IWriter //刷新失败后执行失败回调失败的数据直接写入本地文件系统
	log          ILog
}

type Options struct {
	flushInterval     time.Duration //间隔多少时间向flushChannel发送一个执行信号
	maxLength         int           //最大缓存容量，Cache.length达到这个长度时向flushChannel发送一个执行信号
	log               ILog
	failedFileDir     string //失败落盘目录
	failedFileDirMode os.FileMode
	failedFileName    string //失败落盘文件名称
	enableLocalBackup bool   //是否启用失败后回调失败落盘
}

type Option func(*Options)

const (
	defaultMaxLength         int         = 1000        //本地容量，超过则触发落盘
	defaultFlushInterval                 = time.Minute //本地落盘时间间隔，达到则触发落盘
	defaultFailedFileDir                 = "."         // 落盘回调失败回调失败的备份数据目录
	defaultFailedFileDirMode os.FileMode = 0755
	defaultFailedFileName                = "failed" //失败落盘的文件开始名称
)

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
		writer:       &defaultWriter{opt: opt},
		log:          opt.log,
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

func WithFailedFileDirAndMode(dir string, filename string, mode os.FileMode) Option {
	return func(o *Options) {
		o.enableLocalBackup = dir != ""
		o.failedFileDir = dir
		if dir != "" {
			o.failedFileDir = dir
		}

		if mode != 0 {
			o.failedFileDirMode = mode
		}

		if filename != "" {
			o.failedFileName = defaultFailedFileName
		}
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

	c.log.Debugf("开始刷新数据")

	var err error
	//刷新数据
	if err = c.h.FlushCall(c.data); err == nil {
		c.log.Infof("FlushCall success list total: %v", total)
		return
	} else {
		c.log.Errorf("FlushCall error list total: %v, error: %v", total, err)
	}

	if err = c.h.FailedCall(c.data); err == nil {
		c.log.Infof("FailedCall success list total: %v", total)
		return
	} else {
		c.log.Errorf("FailedCall error list total: %v, error: %v", total, err)
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
	for _, row := range rows {
		body, er := row.Marshal()
		if er != nil {
			c.log.Errorf("failedCallBack json.Marshal error,row: %+v：%v", row, er)
			continue
		}
		_, _ = w.Write(body)
		_, _ = w.WriteString("\n")
	}
	_ = w.Flush()

	return
}

type defaultWriter struct {
	opt      *Options
	curFile  *os.File
	fileName string
}

func (w *defaultWriter) GetWriter() (*os.File, error) {
	filename := filepath.Join(w.opt.failedFileDir, fmt.Sprintf("%s.%s.log", w.opt.failedFileName, time.Now().Format("20060102")))
	if w.curFile != nil && w.fileName == filename {
		return w.curFile, nil
	}

	if w.curFile != nil {
		_ = w.curFile.Close()
	}

	if _, err := os.Stat(w.opt.failedFileDir); os.IsNotExist(err) {
		if err = os.MkdirAll(w.opt.failedFileDir, w.opt.failedFileDirMode); err != nil {
			return nil, err
		}
	}

	file, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	w.curFile = file
	w.fileName = filename
	return file, nil
}

func (w *defaultWriter) Close() error {
	if w.curFile != nil {
		return w.curFile.Close()
	}
	return nil
}
