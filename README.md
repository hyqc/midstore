# 📦 midstore —— 高性能缓存中间件存储模块

`midstore` 是一个基于 Go 的高性能内存缓存模块，支持异步刷新、失败回调和本地落盘功能。适用于日志收集、事件缓冲等需要批量处理数据的场景。

## 🔧 功能特性

- 支持并发安全的数据添加与读取
- 自动定时刷新或达到容量后触发刷新
- 刷新失败自动降级（支持自定义失败回调）
- 可选本地落盘备份，防止数据丢失
- 支持自定义日志接口和配置选项
- 泛型支持，兼容任意类型数据

---

## 🧱 接口定义

### `ICache`

```go
package midstore

type ICache[T Type] interface {
	Add(data T)
	AddList(list []any)
	Len() uint64
	Start()
	Stop()
}
```

缓存的基本操作接口：

| 方法        | 描述          |
|-----------|-------------|
| `Add`     | 添加一条数据到缓存中  |
| `AddList` | 添加一组数据到缓存中  |
| `Len`     | 获取当前缓存的数据数量 |
| `Start`   | 启动后台刷新协程    |
| `Stop`    | 停止后台刷新并释放资源 |

---

### `IHandle[Type]`

```go
type IHandle[T Type] interface {
FlushCall(rows []T) error // 成功返回 nil，失败返回错误
FailedCall(rows []T) error // FlushCall 失败时执行此回调
}
```

用于处理缓存刷新逻辑的接口：

| 方法           | 描述                             |
|--------------|--------------------------------|
| `FlushCall`  | 提交缓存数据，如发送网络请求或写入数据库           |
| `FailedCall` | 如果 `FlushCall` 失败，则调用此方法进行降级处理 |

---

### `ILog`

```go
type ILog interface {
Debugf(format string, v ...any)
Infof(format string, v ...any)
Warnf(format string, v ...any)
Errorf(format string, v ...any)
}
```

统一的日志输出接口，便于集成不同日志库。

---

## 🛠️ 核心结构体

### `Cache[Type]`

```go
type Cache[T Type] struct {
// 内部字段略
}
```

核心缓存结构体，提供以下功能：

- 并发安全的缓存读写
- 定时刷新机制
- 失败回调及本地落盘

---

### `Options`

```go
type Options struct {
flushInterval     time.Duration
maxLength         int
log               ILog
failedFileDir     string
failedFileDirMode os.FileMode
failedFileName    string
enableLocalBackup bool
writer            IWriter
failedBackRows    bool 
}
```

| 字段名                 | 类型              | 描述                                                                     |
|---------------------|-----------------|------------------------------------------------------------------------|
| `flushInterval`     | `time.Duration` | 刷新间隔时间，默认 1 分钟                                                         |
| `maxLength`         | `int`           | 最大缓存条数，超过该值触发刷新，默认 1000                                                |
| `log`               | `ILog`          | 日志接口实例，默认使用内置控制台日志                                                     |
| `failedFileDir`     | `string`        | 刷新失败后的本地文件保存路径，默认当前目录                                                  |
| `failedFileDirMode` | `os.FileMode`   | 文件夹权限配置                                                                |
| `failedFileName`    | `string`        | 失败落盘文件名前缀，示例test，则文件名为 test.xxx.log，其中xxx为日期格式为20060102                |
| `enableLocalBackup` | `bool`          | 是否启用回调失败落盘，默认开启                                                        |
| `writer`            | `IWriter`       | 自定义写入接口                                                                |
| `failedBackRows`    | `bool`          | 回调失败写入磁盘文件的数据格式，示例文件: true时一批一行.20250605.log，false时一批每行一行.20250605.log |

---

## 📌 主要函数与方法

### 创建缓存实例

#### `NewCache[T any]`

```go
func NewCache[T Type](h IHandle[T], opts ...Option) *Cache[T]
```

创建一个新的缓存实例。

**参数说明：**

- `h`: 实现 `IHandle` 接口的对象，用于刷新和失败处理。
- `opts...`: 可选配置项，使用 Option 函数设置。

**示例：**

```go
cache := midstore.NewCache[MyData](myHandler,
midstore.WithMaxLength(500),
midstore.WithFlushInterval(time.Second*30),
)
```

---

### 配置选项函数

#### `WithMaxLength(max int) Option`

设置最大缓存条数。

#### `WithFlushInterval(i time.Duration) Option`

设置定时刷新的时间间隔。

#### `WithLog(l ILog) Option`

设置自定义日志接口。

#### `WithFailedFileDirAndMode(dir string, filename string, mode os.FileMode) Option`

设置失败数据落盘的目录路径,文件名,模式

#### `WithFailedBackRows(t bool) Option`

设置失败写入文件时格式是一批一行还是一行一条，t为true时一批一行

---

### 缓存操作方法

#### `Add(elem T)`

向缓存中添加一条数据，并检查是否满足刷新条件。

#### `AddList(elems []T)`

向缓存中添加一组数据，并检查是否满足刷新条件。

#### `Len() int`

返回当前缓存中的元素数量。

#### `Start()`

启动后台刷新任务，开始监听刷新信号。

#### `Stop()`

停止后台刷新任务，执行最后一次刷新，并关闭相关资源。

#### `flush()`

执行刷新操作，调用 `FlushCall` 提交数据。如果失败则尝试 `FailedCall`，再失败则写入本地文件。

#### `failedCallBack(rows []T)`

将刷新失败的数据写入本地文件系统作为备份。

---

## 📁 示例代码

### 示例1: 初始化并使用 Cache

```go
// 定义元素结构结构体
type elem struct {
Id   int    `json:"id"`
Name string `json:"name"`
}

// 实现元素结构的方法
func (e elem) Marshal() ([]byte, error) {
return json.Marshal(e)
}

// 定义元素落盘处理器
type myHandle struct {
}

func newMyHandle() *myHandle {
return &myHandle{}
}

// 实现落盘回调
func (m *myHandle) FlushCall(rows []elem) error {
for _, e := range rows {
fmt.Println(e)
}
fmt.Println("刷新成功")
return fmt.Errorf("失败1")
}

// 实现落盘失败回调
func (m *myHandle) FailedCall(rows []elem) error {
for _, e := range rows {
fmt.Println(e)
}
fmt.Println("失败回调成功")
return fmt.Errorf("失败2")
}

func TestNewCache(t *testing.T) {
c := NewCache(newMyHandle(),
WithMaxLength(20),
)
c.Start()

ch := make(chan os.Signal, 1)

go func () {
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


```

---

## ✅ 最佳实践建议

- 实现自己的 `IHandle` 接口以适配实际业务逻辑（如发送网络请求、入库等）。
- 使用 `WithLog` 设置更强大的日志框架（如 zap、logrus）。
- 开启 `enableLocalBackup` 并指定 `failedFileDir` 来确保数据不丢失。
- 根据业务需求调整 `flushInterval` 和 `maxLength`，平衡性能与实时性。

---
