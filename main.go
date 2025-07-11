package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mailgun/proxyproto"
)

var (
	logLevel  = 1
	timeout   = time.Duration(10 * time.Second)
	removeEnv = make([]string, 0, 10)
	saneEnv   = false
)

func Msg(level int, format string, args ...any) {
	if level >= logLevel {
		return
	}
	if len(args) > 0 {
		format = fmt.Sprintf(format, args...)
	}
	if _, err := fmt.Fprintln(os.Stderr, format); err != nil {
		panic(err)
	}
}

func Die(format string, args ...any) {
	Msg(0, format, args...)
	os.Exit(1)
}

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

func parseBool[T interface{ ~int | ~bool }](s string, value T, dest *T) error {
	if b, e := strconv.ParseBool(strings.TrimSpace(s)); e != nil {
		return e
	} else if b {
		*dest = value
	}
	return nil
}

func parse_v(s string) error {
	return parseBool(s, 2, &logLevel)
}
func parse_Q(s string) error {
	return parseBool(s, 1, &logLevel)
}
func parse_q(s string) error {
	return parseBool(s, 0, &logLevel)
}
func parse_t(s string) error {
	if t, e := time.ParseDuration(s); e != nil {
		return e
	} else {
		timeout = t
	}
	return nil
}
func parse_x(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return errors.New("while parsing -x: empty varname")
	}
	removeEnv = append(removeEnv, s+"=")
	return nil
}
func parse_X(s string) error {
	return parseBool(s, true, &saneEnv)
}

func addr(a net.Addr) string {
	return a.(*net.TCPAddr).IP.String()
}
func port(a net.Addr) string {
	return strconv.Itoa(a.(*net.TCPAddr).Port)
}

func main() {
	flag.BoolFunc("v", "all available messages", parse_v)
	flag.BoolFunc("Q", "all available error messages; no messages in case of success", parse_Q)
	flag.BoolFunc("q", "no messages in any case", parse_q)
	flag.Func("t", "header read timeout", parse_t)
	flag.Func("x", "eXclude specified environment variable", parse_x)
	flag.BoolFunc("X", "create new empty environment", parse_X)
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		Die("Usage: %s [-v|-q|-Q] [-t <timeout>] <command> [arguments]", os.Args[0])
	}
	command := args[0]
	var (
		err  error
		h    *proxyproto.Header
		i    = 0
		j    = 0
		dEnv []string
		sEnv []string
	)

	if !strings.Contains(command, "/") {
		if command, err = exec.LookPath(command); err != nil {
			Die("error while looking %s in PATH: %v", args[0], err)
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
		err = errors.New("timeout reading proxy protocol header")
	}

	if err != nil {
		Die("error %v", err)
	}

	Msg(1, "version %d header parsed, Local=%v, source=%v, destination=%v, unknown=%v", h.Version, h.IsLocal, h.Source, h.Destination, h.Unknown)

	if !saneEnv {
		sEnv = os.Environ()
	}

	if len(h.Unknown) > 0 || h.IsLocal {
		if saneEnv {
			dEnv = []string{
				"TCPLOCALIP=" + os.Getenv("TCPLOCALIP"),
				"TCPLOCALPORT=" + os.Getenv("TCPLOCALPORT"),
				"TCPREMOTEIP=" + os.Getenv("TCPREMOTEIP"),
				"TCPREMOTEPORT=" + os.Getenv("TCPREMOTEPORT"),
			}
		} else {
			dEnv = sEnv
		}
		goto run
	}

	if h.Source == nil || h.Destination == nil {
		Die("source or destination address is nil, something went wrong")
	}

	dEnv = make([]string, len(sEnv)+4)

env:
	for i = 0; i < len(sEnv); i++ {
		for _, v := range removeEnv {
			if strings.HasPrefix(sEnv[i], v) {
				continue env
			}
		}
		dEnv[j], j = sEnv[i], j+1
	}
	dEnv[j], j = "TCPLOCALIP="+addr(h.Destination), j+1
	dEnv[j], j = "TCPLOCALPORT="+port(h.Destination), j+1
	dEnv[j], j = "TCPREMOTEIP="+addr(h.Source), j+1
	dEnv[j], j = "TCPREMOTEPORT="+port(h.Source), j+1
	dEnv = dEnv[:j]
run:
	Die("error while executing %s: %v", command, syscall.Exec(command, args, dEnv))
}
