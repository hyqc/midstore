package midstore

import (
	"fmt"
	"time"
)

type Log struct {
}

func newLog() *Log {
	return &Log{}
}

func (l *Log) Debugf(format string, v ...interface{}) {
	fmt.Printf("[DEBUG] "+time.Now().Format(time.RFC3339)+format+"\n", v...)
}

func (l *Log) Infof(format string, v ...interface{}) {
	fmt.Printf("[INFO] "+time.Now().Format(time.RFC3339)+format+"\n", v...)
}

func (l *Log) Warnf(format string, v ...interface{}) {
	fmt.Printf("[WARN] "+time.Now().Format(time.RFC3339)+format+"\n", v...)
}

func (l *Log) Errorf(format string, v ...interface{}) {
	fmt.Printf("[ERROR] "+time.Now().Format(time.RFC3339)+format+"\n", v...)
}
