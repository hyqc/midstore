package midstore

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

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

func (w *defaultWriter) OnWriteFailed(data []byte) {
	w.opt.log.Warnf("write failed file error,data: %s", string(data))
}

func (w *defaultWriter) Close() error {
	if w.curFile != nil {
		return w.curFile.Close()
	}
	return nil
}
