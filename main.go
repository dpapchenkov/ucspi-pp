package main

import (
	"errors"
	"flag"
	"fmt"
	"github.com/mailgun/proxyproto"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var (
	logLevel = 1
	timeout  = time.Duration(10 * time.Second)
)

func Msg(level int, format string, args ...any) {
	if level > logLevel {
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

func parseBool(s string, value int, dest *int) error {
	if b, e := strconv.ParseBool(s); e != nil {
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
	sEnv := os.Environ()
	if len(h.Unknown) > 0 || h.IsLocal {
		dEnv = sEnv
		goto run
	}
	if h.Source == nil || h.Destination == nil {
		Die("source or destination address is nil, something went wrong")
	}
	dEnv = make([]string, len(sEnv), len(sEnv)+4)
	for i = 0; i < len(sEnv); i++ {
		switch {
		case strings.HasPrefix(sEnv[i], "TCPLOCALHOST="), strings.HasPrefix(sEnv[i], "TCPREMOTEHOST="):
			continue
		case strings.HasPrefix(sEnv[i], "TCPLOCALIP="), strings.HasPrefix(sEnv[i], "TCPREMOTEIP="):
			continue
		case strings.HasPrefix(sEnv[i], "TCPLOCALPORT="), strings.HasPrefix(sEnv[i], "TCPREMOTEPORT="):
			continue
		default:
			dEnv[j], j = sEnv[i], j+1
		}
	}
	dEnv = append(dEnv[:j], "TCPLOCALIP="+addr(h.Destination), "TCPLOCALPORT="+port(h.Destination), "TCPREMOTEIP="+addr(h.Source), "TCPREMOTEPORT="+port(h.Source))
run:
	Die("error while executing %s: %v", command, syscall.Exec(command, args, dEnv))
}
