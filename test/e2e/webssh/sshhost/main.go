// Command sshhost is a throwaway SSH + SFTP host used only by the WebSSH
// end-to-end harness. It is a separate Go module on purpose: the product must
// never ship its own SSH server, so these dependencies stay out of the root
// go.mod and out of every release binary.
//
// The shell request runs a real pty, because lrzsz (sz/rz) refuses to start
// without a tty. That is what makes the ZMODEM assertions meaningful.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/creack/pty"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const (
	testUser = "tmuser"
	testPass = "tmpass"
	// defaultListen keeps the harness off the real sshd port. Override with
	// TM_SSHHOST_LISTEN when 2222 is already taken.
	defaultListen = "127.0.0.1:2222"
)

// envOr returns the environment value or a fallback, so every machine-specific
// choice (listen address, shell, extra PATH entries) can be set by the harness
// instead of being compiled in.
func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// defaultShell picks something runnable on any platform: the caller's shell
// first, then bash, then POSIX sh.
func defaultShell() string {
	for _, candidate := range []string{os.Getenv("TM_SSHHOST_SHELL"), os.Getenv("SHELL"), "/bin/bash", "/bin/sh"} {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "/bin/sh"
}

// workDir is where every shell lands, so an `rz` upload can be verified from the
// harness without touching the rest of the machine.
var workDir = os.Getenv("TM_SSHHOST_CWD")

func main() {
	if workDir == "" {
		dir, err := os.MkdirTemp("", "tm-sshhost-")
		if err != nil {
			log.Fatal(err)
		}
		workDir = dir
	}
	log.Printf("sshhost workdir=%s", workDir)
	// The SFTP subsystem is rooted at the process cwd, so the whole throwaway
	// host (shell and SFTP) operates inside one inspectable directory.
	if err := os.Chdir(workDir); err != nil {
		log.Fatal(err)
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		log.Fatal(err)
	}

	config := &ssh.ServerConfig{
		PasswordCallback: func(meta ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if meta.User() == testUser && string(pass) == testPass {
				return nil, nil
			}
			return nil, fmt.Errorf("password rejected for %q", meta.User())
		},
	}
	config.AddHostKey(signer)

	listen := envOr("TM_SSHHOST_LISTEN", defaultListen)
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("sshhost listening on %s workdir=%s\n", listen, workDir)

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		go serve(conn, config)
	}
}

func serve(conn net.Conn, config *ssh.ServerConfig) {
	sshConn, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		log.Printf("handshake: %v", err)
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(requests)
	log.Printf("ssh conn established remote=%s", sshConn.RemoteAddr())

	for newChannel := range channels {
		log.Printf("channel open type=%s", newChannel.ChannelType())
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "only session channels supported")
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			log.Printf("accept channel: %v", err)
			continue
		}
		log.Printf("channel accepted")
		go serveSession(channel, requests)
	}
}

func serveSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()
	size := &pty.Winsize{Rows: 24, Cols: 80}
	for req := range requests {
		log.Printf("session request type=%s wantReply=%v", req.Type, req.WantReply)
		switch req.Type {
		case "pty-req":
			if win, term := parsePtyReq(req.Payload); win != nil {
				size = win
				log.Printf("pty term=%s size=%dx%d", term, win.Cols, win.Rows)
			}
			_ = req.Reply(true, nil)
		case "window-change":
			_ = req.Reply(true, nil)
		case "env":
			_ = req.Reply(true, nil)
		case "shell":
			_ = req.Reply(true, nil)
			ptyShell(channel, requests, size)
			return
		case "subsystem":
			if subsystemName(req.Payload) != "sftp" {
				_ = req.Reply(false, nil)
				continue
			}
			_ = req.Reply(true, nil)
			// Read-only by default keeps a manually started throwaway host from
			// ever mutating the machine; the harness opts into writes because its
			// SFTP root is a temp directory it owns.
			var serverOptions []sftp.ServerOption
			if envOr("TM_SSHHOST_SFTP_READONLY", "1") != "0" {
				serverOptions = append(serverOptions, sftp.ReadOnly())
			}
			server, err := sftp.NewServer(channel, serverOptions...)
			if err != nil {
				log.Printf("sftp server: %v", err)
				return
			}
			log.Printf("sftp serving started")
			if err := server.Serve(); err != nil && err != io.EOF {
				log.Printf("sftp serve: %v", err)
			}
			log.Printf("sftp serving ended")
			return
		default:
			_ = req.Reply(false, nil)
		}
	}
}

// ptyShell runs the user's real shell on a pty and pipes it to the SSH channel.
// lrzsz needs a tty: without one, sz/rz refuse to start.
func ptyShell(channel ssh.Channel, requests <-chan *ssh.Request, size *pty.Winsize) {
	shell := defaultShell()
	cmd := exec.Command(shell, "-i")
	cmd.Dir = workDir
	// lrzsz is usually installed outside the default PATH (Homebrew on macOS,
	// /usr/local/bin elsewhere), so the harness can extend it explicitly.
	path := os.Getenv("PATH")
	if extra := envOr("TM_SSHHOST_EXTRA_PATH", ""); extra != "" {
		path += ":" + extra
	}
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"PATH="+path,
		"PS1=tmhost$ ",
		"PROMPT=tmhost$ ",
	)
	ptmx, err := pty.StartWithSize(cmd, size)
	if err != nil {
		log.Printf("pty start: %v", err)
		_, _ = channel.Write([]byte("pty start failed\r\n"))
		return
	}
	log.Printf("pty shell started pid=%d", cmd.Process.Pid)

	var once sync.Once
	closeAll := func() {
		once.Do(func() {
			_ = ptmx.Close()
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			_ = channel.Close()
		})
	}

	// channel -> pty (keystrokes)
	go func() {
		if _, err := io.Copy(ptmx, channel); err != nil {
			log.Printf("channel->pty: %v", err)
		}
		closeAll()
	}()
	// pty -> channel (shell output, including ZMODEM frames)
	go func() {
		if _, err := io.Copy(channel, ptmx); err != nil {
			log.Printf("pty->channel: %v", err)
		}
		closeAll()
	}()
	// window-change resizing
	go func() {
		for req := range requests {
			if req.Type == "window-change" {
				if win, _ := parseWindowChange(req.Payload); win != nil {
					_ = pty.Setsize(ptmx, win)
				}
			}
			if req.WantReply {
				_ = req.Reply(true, nil)
			}
		}
	}()

	if err := cmd.Wait(); err != nil {
		log.Printf("shell exited: %v", err)
	}
	log.Printf("pty shell finished")
	closeAll()
}

func parsePtyReq(payload []byte) (*pty.Winsize, string) {
	term, rest, ok := decodeString(payload)
	if !ok {
		return nil, ""
	}
	if win, ok := parseWindowChange(rest); ok {
		return win, term
	}
	return nil, term
}

func parseWindowChange(payload []byte) (*pty.Winsize, bool) {
	if len(payload) < 16 {
		return nil, false
	}
	return &pty.Winsize{
		Rows: uint16(binary.BigEndian.Uint32(payload[0:4])),
		Cols: uint16(binary.BigEndian.Uint32(payload[4:8])),
	}, true
}

func decodeString(payload []byte) (string, []byte, bool) {
	if len(payload) < 4 {
		return "", nil, false
	}
	length := binary.BigEndian.Uint32(payload[:4])
	if uint32(len(payload)) < 4+length {
		return "", nil, false
	}
	return string(payload[4 : 4+length]), payload[4+length:], true
}

func subsystemName(payload []byte) string {
	name, _, ok := decodeString(payload)
	if !ok {
		return ""
	}
	return strings.TrimSpace(name)
}
