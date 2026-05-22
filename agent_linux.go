//go:build linux

package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"

	"github.com/mdlayher/vsock"

	observability "github.com/rawtreedb/rawtree-firecracker/internal"
)

func runGuestAgent(port uint32) error {
	listener, err := vsock.Listen(port, nil)
	if err != nil {
		return fmt.Errorf("listen on guest vsock port %d: %w", port, err)
	}
	defer listener.Close()

	for {
		conn, err := listener.Accept()
		if err != nil {
			return fmt.Errorf("accept vsock connection: %w", err)
		}
		go handleGuestAgentConnection(conn)
	}
}

func handleGuestAgentConnection(conn net.Conn) {
	defer conn.Close()

	decoder := json.NewDecoder(conn)
	encoder := json.NewEncoder(conn)
	encoderMu := sync.Mutex{}

	request := observability.ExecRequest{}
	if err := decoder.Decode(&request); err != nil {
		writeAgentFrame(encoder, &encoderMu, observability.ExecFrame{
			Type:    "error",
			Message: fmt.Sprintf("decode exec request: %v", err),
		})
		return
	}

	exitCode, err := runGuestCommand(request, decoder, encoder, &encoderMu)
	if err != nil {
		writeAgentFrame(encoder, &encoderMu, observability.ExecFrame{
			Type:     "error",
			ExitCode: exitCode,
			Message:  err.Error(),
		})
		return
	}

	writeAgentFrame(encoder, &encoderMu, observability.ExecFrame{
		Type:     "exit",
		ExitCode: exitCode,
	})
}

func runGuestCommand(
	request observability.ExecRequest,
	decoder *json.Decoder,
	encoder *json.Encoder,
	encoderMu *sync.Mutex,
) (int, error) {
	if len(request.Argv) == 0 {
		return 127, fmt.Errorf("empty command")
	}

	argv := request.Argv
	if request.Sudo && os.Geteuid() != 0 {
		argv = append([]string{"sudo", "-n"}, argv...)
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	if request.Workdir != "" {
		cmd.Dir = request.Workdir
	}
	cmd.Env = os.Environ()
	for key, value := range request.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return 127, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 127, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return 127, err
	}

	if err := cmd.Start(); err != nil {
		return 127, err
	}

	writeAgentFrame(encoder, encoderMu, observability.ExecFrame{
		Type: "started",
		PID:  cmd.Process.Pid,
	})

	var outputWG sync.WaitGroup
	outputWG.Add(2)
	go copyGuestCommandOutput("stdout", stdout, encoder, encoderMu, &outputWG)
	go copyGuestCommandOutput("stderr", stderr, encoder, encoderMu, &outputWG)

	go func() {
		defer stdin.Close()
		if !request.Interactive {
			return
		}
		for {
			frame := observability.ExecFrame{}
			if err := decoder.Decode(&frame); err != nil {
				return
			}
			if frame.Type == "stdin_eof" {
				return
			}
			if frame.Type != "stdin" {
				continue
			}
			payload, err := base64.StdEncoding.DecodeString(frame.Data)
			if err != nil {
				continue
			}
			_, _ = stdin.Write(payload)
		}
	}()

	waitErr := cmd.Wait()
	outputWG.Wait()

	if waitErr == nil {
		return 0, nil
	}

	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return 1, waitErr
}

func copyGuestCommandOutput(
	stream string,
	reader io.Reader,
	encoder *json.Encoder,
	encoderMu *sync.Mutex,
	wg *sync.WaitGroup,
) {
	defer wg.Done()

	buffer := make([]byte, 32*1024)
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			writeAgentFrame(encoder, encoderMu, observability.ExecFrame{
				Type: stream,
				Data: base64.StdEncoding.EncodeToString(buffer[:n]),
			})
		}
		if err != nil {
			return
		}
	}
}

func writeAgentFrame(encoder *json.Encoder, mu *sync.Mutex, frame observability.ExecFrame) {
	mu.Lock()
	defer mu.Unlock()
	_ = encoder.Encode(frame)
}
