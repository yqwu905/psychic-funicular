package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// jobLogStore 把作业日志按 <dir>/<jobid>.log 存盘。
type jobLogStore struct {
	dir string
}

func newJobLogStore(dir string) (*jobLogStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create logs dir: %w", err)
	}
	return &jobLogStore{dir: dir}, nil
}

// path 返回作业日志文件路径；用 Base 防止 jobID 携带路径分隔符。
func (j *jobLogStore) path(jobID string) (string, error) {
	if jobID == "" || strings.ContainsAny(jobID, "/\\") {
		return "", fmt.Errorf("invalid job id")
	}
	return filepath.Join(j.dir, filepath.Base(jobID)+".log"), nil
}

func (j *jobLogStore) append(jobID string, data []byte) error {
	p, err := j.path(jobID)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
}

func (j *jobLogStore) open(jobID string) (*os.File, error) {
	p, err := j.path(jobID)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}
