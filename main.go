package main

import (
	"encoding/hex"
	"io"
	"log/syslog"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dmgk/getopt"
	"github.com/mailgun/proxyproto"
)

type Str interface {
	String() string
}

var (
	logLevel   = syslog.LOG_ERR
	timeout    = time.Duration(10 * time.Second)
	removeEnv  = make([]string, 0, 10)
	cleanEnv   = false
	logDest    = io.Writer(os.Stderr)
	useSyslog  = false
	useJournal = false
	me         = os.Args[0]
)

func safestr(s Str) string {
	if s == nil {
		return ""
	} else {
		return s.String()
	}
}

func Log(priority syslog.Priority, message string) {
	if priority > logLevel {
		return
	}
	var err error
	if !useSyslog && !useJournal {
		message = me + ": " + message
	}
	message += "\n"
	if useJournal {
		message = "<" + strconv.Itoa(int(priority)) + ">" + message
	}
	if useSyslog {
		switch priority {
		case syslog.LOG_EMERG:
			err = logDest.(*syslog.Writer).Emerg(message)
		case syslog.LOG_ALERT:
			err = logDest.(*syslog.Writer).Alert(message)
		case syslog.LOG_CRIT:
			err = logDest.(*syslog.Writer).Crit(message)
		case syslog.LOG_ERR:
			err = logDest.(*syslog.Writer).Err(message)
		case syslog.LOG_WARNING:
			err = logDest.(*syslog.Writer).Warning(message)
		case syslog.LOG_NOTICE:
			err = logDest.(*syslog.Writer).Notice(message)
		case syslog.LOG_INFO:
			err = logDest.(*syslog.Writer).Info(message)
		case syslog.LOG_DEBUG:
			err = logDest.(*syslog.Writer).Debug(message)
		default:
			panic("wrong peiority")
		}
	} else {
		_, err = logDest.Write([]byte(message))
	}
	if err != nil {
		panic(err)
	}
}

func usage() {
	os.Stderr.Write([]byte("Usage: " + me + "[-v|-Q|-q] [-s|-j] [-X] [-x <varname> ...] <command> [arguments]"))
	os.Stderr.Write([]byte(
		`
-v show all availible messages
-Q show only error messages
-q no messages at all
-s use syslog.
-j use systemd-journald format
-X spawn <command> in clean environment (only preserve TCPLOCAL\REMOTE ADDR\PORT for LOCAL connections)
-x remove <varname> before spawning <command>
-t timeout for proxy header
-h print this help
`))
}

func Die(message string, code int) {
	os.Stderr.Write([]byte(me + ": " + message))
	os.Exit(code)
}

func addr(a net.Addr) string {
	return a.(*net.TCPAddr).IP.String()
}
func port(a net.Addr) string {
	return strconv.Itoa(a.(*net.TCPAddr).Port)
}

func main() {
	var (
		err     error
		opts    *getopt.Scanner
		h       *proxyproto.Header
		i       = 0
		j       = 0
		sEnv    []string
		dEnv    []string
		command string
		args    []string
	)

	if opts, err = getopt.New("hjqQvXst:x:"); err != nil {
		Die("error while creating scanner: "+err.Error(), 1)
	}
	me = opts.ProgramName()
	// [options] are processed by the getopt standard; thus an argument of --
	// terminates [options]. [tool] supports three options to control how
	// much information it prints to stderr:
	//
	//   -v all available messages
	//   -Q all available error messages; no messages in case of success
	//   -q no messages in any case
	//
	// The default is -Q; later arguments override earlier arguments. [tool]
	// may support many further options.

	for opts.Scan() {
		var opt *getopt.Option
		if opt, err = opts.Option(); err != nil {
			os.Stderr.Write([]byte(me + "error parsing option: " + err.Error()))
			usage()
			os.Exit(2)
		}
		switch opt.Opt {
		case 'h':
			usage()
			os.Exit(0)
			continue
		case 'j':
			useJournal, useSyslog = true, false
			continue
		case 's':
			useSyslog, useJournal = true, false
			continue
		case 'q':
			logLevel = -1
			continue
		case 'Q':
			logLevel = syslog.LOG_ERR
			continue
		case 'v':
			logLevel = syslog.LOG_DEBUG
			continue
		case 't':
			if t, e := time.ParseDuration(*opt.Arg); e != nil {
				os.Stderr.Write([]byte(me + "error parsing -t: " + err.Error()))
				usage()
				os.Exit(2)
			} else {
				timeout = t
			}
			continue
		case 'x':
			removeEnv = append(removeEnv, *opt.Arg+"=")
			continue
		case 'X':
			cleanEnv = true
			continue
		}
	}

	args = opts.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	command = args[0]

	if useSyslog {
		logDest, err = syslog.New(logLevel|syslog.LOG_DAEMON, me)
		if err != nil {
			Die("could not connect to syslog: "+err.Error(), 1)
		}
	}

	if !strings.Contains(command, "/") {
		if command, err = exec.LookPath(command); err != nil {
			Log(syslog.LOG_ERR, "error while searching "+args[0]+" in PATH: "+err.Error())
			os.Exit(1)
		}
	}

	done := make(chan bool, 1)

	go func() {
		h, err = proxyproto.ReadHeader(os.Stdin)
		done <- true
	}()

	select {
	case <-done:
		break
	case <-time.After(timeout):
		Log(syslog.LOG_ERR, "timeout reading proxy protocol header")
		os.Exit(1)
	}
	Log(syslog.LOG_INFO,
		"version "+strconv.Itoa(h.Version)+
			" header parsed, Local="+strconv.FormatBool(h.IsLocal)+
			", source="+safestr(h.Source)+
			", destination="+safestr(h.Destination)+
			", unknown="+hex.EncodeToString(h.Unknown))
	if !cleanEnv || h.IsLocal || len(h.Unknown) > 0 {
		sEnv = syscall.Environ()
	}

	dEnv = make([]string, len(sEnv)+4)

env:
	for i = 0; i < len(sEnv); i++ {
		if cleanEnv {
			if strings.HasPrefix(sEnv[i], "TCPLOCALIP=") || strings.HasPrefix(sEnv[i], "TCPLOCALPORT=") || strings.HasPrefix(sEnv[i], "TCPREMOTEIP=") || strings.HasPrefix(sEnv[i], "TCPREMOTEPORT=") {
				goto copyenv
			} else {
				continue env
			}
		}
		for _, v := range removeEnv {
			if strings.HasPrefix(sEnv[i], v) {
				continue env
			}
		}
	copyenv:
		dEnv[j], j = sEnv[i], j+1
	}
	if !h.IsLocal && len(h.Unknown) == 0 {
		if h.Source == nil || h.Destination == nil {
			Log(syslog.LOG_ERR, "source or destination address is nil, something went wrong")
			os.Exit(1)
		} else {
			dEnv[j], j = "TCPLOCALIP="+addr(h.Destination), j+1
			dEnv[j], j = "TCPLOCALPORT="+port(h.Destination), j+1
			dEnv[j], j = "TCPREMOTEIP="+addr(h.Source), j+1
			dEnv[j], j = "TCPREMOTEPORT="+port(h.Source), j+1
		}
	}
	err = syscall.Exec(command, args, dEnv[:j])
	Log(syslog.LOG_ERR, "error while executing "+command+": "+err.Error())
}
