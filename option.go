package midstore

import (
	"os"
	"time"
)

type Options struct {
	flushInterval     time.Duration //间隔多少时间向flushChannel发送一个执行信号
	maxLength         int           //最大缓存容量，Cache.length达到这个长度时向flushChannel发送一个执行信号
	log               ILog
	failedFileDir     string //失败落盘目录
	failedFileDirMode os.FileMode
	failedFileName    string //失败落盘文件名称
	enableLocalBackup bool   //是否启用失败后回调失败落盘
	writer            IWriter
	failedBackRows    bool //true:一批一行,false:一批N行
}

type Option func(*Options)

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

// WithLog 自定义日志
func WithLog(l ILog) Option {
	return func(o *Options) {
		if l == nil {
			l = newLog()
		}
		o.log = l
	}
}

// WithFailedBackRows 配置失败备份一批一行记录
func WithFailedBackRows(t bool) Option {
	return func(o *Options) {
		o.failedBackRows = t
	}
}

// WithFailedFileDirAndMode 配置失败备份磁盘目录、文件名前缀、文件夹创建模式
func WithFailedFileDirAndMode(dir string, filename string, mode os.FileMode) Option {
	return func(o *Options) {
		o.enableLocalBackup = dir != ""
		if dir != "" {
			o.failedFileDir = dir
		}

		if mode != 0 {
			o.failedFileDirMode = mode
		}

		if filename != "" {
			o.failedFileName = filename
		}
	}
}
