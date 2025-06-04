package midstore

import "os"

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
