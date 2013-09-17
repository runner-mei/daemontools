package daemontools

import (
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"runtime"
	"strconv"
)

var pidFile *string

func init() {
	if "windows" == runtime.GOOS {
		pidFile = flag.String("pid_file", "daemontools.pid", "File containing process PID")
	} else {
		pidFile = flag.String("pid_file", "/var/run/daemontools.pid", "File containing process PID")
	}
}

func isPidInitialize() bool {
	ret := false
	flag.Visit(func(f *flag.Flag) {
		if "pid_file" == f.Name {
			ret = true
		}
	})
	return ret
}

func createPidFile(pidFile string) error {
	if pidString, err := ioutil.ReadFile(pidFile); err == nil {
		pid, err := strconv.Atoi(string(pidString))
		if err == nil {
			if _, err = os.FindProcess(pid); nil == err {
				return fmt.Errorf("pid file found, ensure "+pidFile+" is not running or delete %s", pidFile)
			}
		}
	}

	file, err := os.Create(pidFile)
	if err != nil {
		return err
	}

	defer file.Close()

	_, err = fmt.Fprintf(file, "%d", os.Getpid())
	return err
}

func removePidFile(pidFile string) {
	if err := os.Remove(pidFile); err != nil {
		fmt.Printf("Error removing %s: %s\r\n", pidFile, err)
	}
}