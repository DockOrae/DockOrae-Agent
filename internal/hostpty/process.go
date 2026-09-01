package hostpty

import (
	"io"
	"os"
	"os/exec"
)

type Process interface {
	io.ReadWriteCloser
	Wait() error
	Kill() error
	Resize(rows, columns uint16) error
}

func Start(command *exec.Cmd, rows, columns uint16) (Process, error) {
	return startPlatform(command, rows, columns)
}

func CreateInput(path string) error {
	return createPlatformInput(path)
}

func OpenInput(path string) (*os.File, error) {
	return openPlatformInput(path)
}

func WriteInput(path string, data []byte) error {
	return writePlatformInput(path, data)
}

func RemoveInput(path string) error {
	return removePlatformInput(path)
}

func IsEnd(err error) bool {
	return isPlatformEnd(err)
}
