package sshclient

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	gopath "path"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

type Client struct {
	conn *ssh.Client
	host string
	done chan struct{}
}

func New(host, user string, useAgent bool, privateKey string) (*Client, error) {
	var method ssh.AuthMethod;
	
	if !useAgent {
		signer, err := ssh.ParsePrivateKey([]byte(privateKey))
		if err != nil {
			return nil, fmt.Errorf("parsing SSH key: %w", err)
		}

		method = ssh.PublicKeys(signer)
	} else {
		socket := os.Getenv("SSH_AUTH_SOCK")
		agentConn, err := net.Dial("unix", socket)
		if err != nil {
			return nil, fmt.Errorf("open SSH_AUTH_SOCK: %v", err)
		}

		agentClient := agent.NewClient(agentConn)
		method = ssh.PublicKeysCallback(agentClient.Signers)
	}

	config := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{
			method,
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	conn, err := ssh.Dial("tcp", host+":22", config)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", host, err)
	}

	c := &Client{conn: conn, host: host, done: make(chan struct{})}

	// Keepalive to prevent timeouts during long builds
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-c.done:
				return
			case <-ticker.C:
				conn.SendRequest("keepalive@openssh.com", true, nil)
			}
		}
	}()

	return c, nil
}

func (c *Client) Host() string {
	return c.host
}

func (c *Client) Close() error {
	close(c.done)
	return c.conn.Close()
}

// Run executes a command and returns stdout, stderr, and any error.
func (c *Client) Run(cmd string) (string, string, error) {
	session, err := c.conn.NewSession()
	if err != nil {
		return "", "", fmt.Errorf("creating session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	err = session.Run(cmd)
	return stdout.String(), stderr.String(), err
}

// RunStreaming executes a command, calling onOutput for each line of combined
// stdout/stderr output. Returns an error if the command exits non-zero.
func (c *Client) RunStreaming(cmd string, onOutput func(string)) error {
	session, err := c.conn.NewSession()
	if err != nil {
		return fmt.Errorf("creating session: %w", err)
	}
	defer session.Close()

	stdoutPipe, err := session.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := session.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := session.Start(cmd); err != nil {
		return fmt.Errorf("starting %q: %w", cmd, err)
	}

	var wg sync.WaitGroup
	var stderrBuf strings.Builder

	readPipe := func(r io.Reader, captureStderr bool) {
		defer wg.Done()
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if captureStderr {
				stderrBuf.WriteString(line + "\n")
			}
			if onOutput != nil {
				onOutput(line)
			}
		}
	}

	wg.Add(2)
	go readPipe(stdoutPipe, false)
	go readPipe(stderrPipe, true)
	wg.Wait()

	if err := session.Wait(); err != nil {
		errMsg := stderrBuf.String()
		if errMsg != "" {
			return fmt.Errorf("%s failed:\n%s", cmd, errMsg)
		}
		return fmt.Errorf("%s: %w", cmd, err)
	}
	return nil
}

// WriteFiles writes a map of relative-path → content into baseDir on the remote
// host. The remote directory is cleaned before writing.
func (c *Client) WriteFiles(baseDir string, files map[string]string) error {
	// Clean target directory for consistent state
	c.Run(fmt.Sprintf("rm -rf %s", baseDir))

	sftpClient, err := sftp.NewClient(c.conn)
	if err != nil {
		return fmt.Errorf("creating SFTP client: %w", err)
	}
	defer sftpClient.Close()

	for relPath, content := range files {
		remotePath := baseDir + "/" + relPath

		parentDir := gopath.Dir(remotePath)
		if err := sftpClient.MkdirAll(parentDir); err != nil {
			return fmt.Errorf("creating directory %s: %w", parentDir, err)
		}

		f, err := sftpClient.Create(remotePath)
		if err != nil {
			return fmt.Errorf("creating %s: %w", remotePath, err)
		}
		_, writeErr := f.Write([]byte(content))
		f.Close()
		if writeErr != nil {
			return fmt.Errorf("writing %s: %w", remotePath, writeErr)
		}
	}
	return nil
}

// WriteFile writes content to a single file on the remote host with the given
// permissions.
func (c *Client) WriteFile(remotePath string, content []byte, mode os.FileMode) error {
	sftpClient, err := sftp.NewClient(c.conn)
	if err != nil {
		return fmt.Errorf("creating SFTP client: %w", err)
	}
	defer sftpClient.Close()

	parentDir := gopath.Dir(remotePath)
	if err := sftpClient.MkdirAll(parentDir); err != nil {
		return fmt.Errorf("creating directory %s: %w", parentDir, err)
	}

	f, err := sftpClient.Create(remotePath)
	if err != nil {
		return fmt.Errorf("creating %s: %w", remotePath, err)
	}
	defer f.Close()

	if _, err := f.Write(content); err != nil {
		return fmt.Errorf("writing %s: %w", remotePath, err)
	}

	if err := sftpClient.Chmod(remotePath, mode); err != nil {
		return fmt.Errorf("chmod %s: %w", remotePath, err)
	}

	return nil
}
