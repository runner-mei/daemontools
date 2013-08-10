package daemontools

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

type supervisor_default struct {
	supervisorBase
	success_flag string

	proc_status int32
	pid         int
	stdin       io.Writer

	lock sync.Mutex
	cond *sync.Cond
	once sync.Once
}

func (self *supervisor_default) stats() map[string]interface{} {
	self.init()
	pid := 0
	self.cond.L.Lock()
	pid = self.pid
	self.cond.L.Unlock()

	srv_status := srvString(atomic.LoadInt32(&self.srv_status))
	proc_status := procString(atomic.LoadInt32(&self.proc_status))

	res := self.supervisorBase.stats()

	res["pid"] = pid
	res["success_flag"] = self.success_flag
	res["status"] = srv_status + " " + proc_status
	res["srv_status"] = srv_status
	res["proc_status"] = proc_status
	return res
}

func (self *supervisor_default) casStatus(old_status, new_status int32) bool {
	if !atomic.CompareAndSwapInt32(&self.srv_status, old_status, new_status) {
		return false
	}

	self.cond.Broadcast()
	return true
}

func (self *supervisor_default) setStatus(new_status int32) {
	atomic.StoreInt32(&self.srv_status, new_status)
	self.cond.Broadcast()
}

func (self *supervisor_default) untilStarted() error {
	return self.untilWith(SRV_STARTING, SRV_RUNNING)
}

func (self *supervisor_default) untilStopped() error {
	return self.untilWith(SRV_STOPPING, SRV_INIT)
}

func (self *supervisor_default) untilWith(old_status, srv_status int32) error {
	self.init()

	self.cond.L.Lock()
	defer self.cond.L.Unlock()

	for {
		s := atomic.LoadInt32(&self.srv_status)
		switch s {
		case srv_status:
			return nil
		case old_status:
			break
		default:
			return fmt.Errorf("status is invalid, old_status is %v, excepted is %v, actual is %v.",
				srvString(old_status), srvString(srv_status), srvString(s))
		}
		self.cond.Wait()
	}
}

func (self *supervisor_default) init() {
	self.once.Do(func() {
		self.cond = sync.NewCond(&self.lock)
	})
}

func (self *supervisor_default) start() {
	self.init()

	if !self.casStatus(SRV_INIT, SRV_STARTING) {
		return
	}

	go self.loop()
}

func (self *supervisor_default) stop() {
	self.init()
	self.logString(time.Now().String() + " [sys]swithing to '" + srvString(atomic.LoadInt32(&self.srv_status)) + "'\r\n")
	if !self.casStatus(SRV_RUNNING, SRV_STOPPING) &&
		!self.casStatus(SRV_STARTING, SRV_STOPPING) {
		return
	}
	self.logString(time.Now().String() + " [sys]swith to '" + srvString(atomic.LoadInt32(&self.srv_status)) + "'\r\n")
	go self.interrupt()
}

func (self *supervisor_default) interrupt() {
	pid := 0
	self.cond.L.Lock()
	pid = self.pid
	self.cond.L.Unlock()

	if 0 == pid {
		self.logString(time.Now().String() + " [sys] pid = 0\r\n")
		return
	}
	var ok bool
	var txt string

	if nil != self.stop_cmd {
		switch self.stop_cmd.proc {
		case "__kill___", "":
			goto end
		case "__signal__":
			ok, txt = self.killBySignal(pid)
		case "__console__":
			ok, txt = self.killByConsole(pid)
		default:
			ok, txt = self.killByCmd(pid)
		}

		if ok {
			if nil != self.out && 0 != len(txt) {
				if *is_print {
					fmt.Print(txt)
				} else {
					io.WriteString(self.out, txt)
				}
			}
			return
		}
	}
end:
	e := kill(pid)
	if 0 != len(txt) {
		txt = txt + "\r\n"
	}
	if nil != e {
		txt = txt + "[sys]" + e.Error() + "\r\n"
	} else {
		txt = txt + "[sys] kill process when exit\r\n"
	}

	if 0 != len(txt) {
		self.logString(txt)
	}
}

func (self *supervisor_default) killByConsole(pid int) (bool, string) {

	if nil == self.stop_cmd.arguments || 0 == len(self.stop_cmd.arguments) {
		return false, "console arguments is empty"
	}

	e := func() error {
		self.cond.L.Lock()
		defer self.cond.L.Unlock()
		if nil == self.stdin {
			return errors.New("stdin is not redirect.")
		}

		for _, s := range self.stop_cmd.arguments {
			_, e := self.stdin.Write([]byte(s + "\r\n"))
			if nil != e {
				return e
			}
		}
		return nil
	}()

	if nil != e {
		return false, e.Error()
	}

	pr, e := os.FindProcess(pid)
	if nil != e {
		return false, e.Error()
	}
	e = waitWithTimeout(self.killTimeout, pr)
	if nil != e {
		return false, e.Error()
	}
	return true, ""
}

func (self *supervisor_default) loop() {
	defer func() {
		self.cond.L.Lock()
		self.stdin = nil
		self.pid = 0
		self.cond.L.Unlock()

		self.setStatus(SRV_INIT)
		atomic.StoreInt32(&self.proc_status, PROC_INIT)

		if e := recover(); nil != e {
			var buffer bytes.Buffer
			buffer.WriteString(fmt.Sprintf("[panic] crashed with error - %s\r\n", e))
			for i := 1; ; i += 1 {
				_, file, line, ok := runtime.Caller(i)
				if !ok {
					break
				}
				buffer.WriteString(fmt.Sprintf("    %s:%d\r\n", file, line))
			}
			self.logString(buffer.String())
		}

		self.logString("[sys] ====================  srv  end  ====================\r\n")
	}()

	self.logString("[sys] ==================== srv  start ====================\r\n")
	for i := 0; i < self.retries; i++ {
		self.run(func() {
			self.casStatus(SRV_STARTING, SRV_RUNNING)
		})
		if SRV_STARTING != atomic.LoadInt32(&self.srv_status) {
			break
		}

		self.logString(time.Now().String() + " [sys]current status is '" + srvString(atomic.LoadInt32(&self.srv_status)) + "'\r\n")
	}

	for SRV_RUNNING == atomic.LoadInt32(&self.srv_status) {
		self.logString(time.Now().String() + " [sys]current status is '" + srvString(atomic.LoadInt32(&self.srv_status)) + "'\r\n")
		time.Sleep(2 * time.Second)
		self.run(nil)
	}
}

func (self *supervisor_default) run(cb func()) {
	self.cond.L.Lock()
	is_locked := true
	defer func() {

		if !is_locked {
			self.cond.L.Lock()
		}
		self.stdin = nil
		self.pid = 0
		self.cond.L.Unlock()

		atomic.StoreInt32(&self.proc_status, PROC_INIT)

		if e := recover(); nil != e {
			var buffer bytes.Buffer
			buffer.WriteString(fmt.Sprintf("[panic] crashed with error - %s\r\n", e))
			for i := 1; ; i += 1 {
				_, file, line, ok := runtime.Caller(i)
				if !ok {
					break
				}
				buffer.WriteString(fmt.Sprintf("    %s:%d\r\n", file, line))
			}

			self.logString(buffer.String())
		}
		self.logString("[sys] --------------------  proc end  --------------------\r\n")
	}()

	if st := atomic.LoadInt32(&self.srv_status); SRV_RUNNING != st && SRV_STARTING != st {
		return
	}

	self.logString("[sys] -------------------- proc start --------------------\r\n")
	atomic.StoreInt32(&self.proc_status, PROC_STARTING)

	cmd := self.start_cmd.command()
	if 0 == len(self.success_flag) {
		if *is_print {
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
		} else {
			cmd.Stdout = self.out
			cmd.Stderr = self.out
		}
		if nil != cb {
			cb()
		}
	} else {
		wrapped := wrap(self.out, []byte(self.success_flag), cb)
		cmd.Stdout = wrapped
		cmd.Stderr = wrapped
	}

	var in io.Writer = nil
	var e error = nil
	if nil != self.stop_cmd && "__console__" == self.stop_cmd.proc {
		in, e = cmd.StdinPipe()
		if nil != e {
			self.logString(fmt.Sprintf("[sys] create pipe failed for stdin - %v\r\n", e))
		}
	}

	self.logString(fmt.Sprintf("[sys] %v\r\n", cmd.Path))
	for idx, s := range cmd.Args {
		if 0 == idx {
			continue
		}
		self.logString(fmt.Sprintf("[sys] \t\t%v\r\n", s))
	}

	if e = cmd.Start(); nil != e {
		self.logString(fmt.Sprintf("[sys] start process failed - %v\r\n", e))
		return
	}
	atomic.StoreInt32(&self.proc_status, PROC_RUNNING)

	self.stdin = in
	self.pid = cmd.Process.Pid
	self.cond.L.Unlock()
	is_locked = false

	if e = cmd.Wait(); nil != e {
		self.logString(fmt.Sprintf("[sys] wait process failed - %v\r\n", e))
		return
	}
}