package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	appBinary     = "/app/sub2api"
	workerHost    = "127.0.0.1"
	workerPort    = "19090"
	workerReady   = "http://" + workerHost + ":" + workerPort + "/health/ready"
	workerToken   = "OPENCODE_FREE_BUN_TOKEN"
	workerEnabled = "OPENCODE_FREE_BUN_ENABLED"
)

func main() {
	log.SetPrefix("opencode-supervisor: ")
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		args = append([]string{appBinary}, args...)
	}
	bundled, _ := strconv.ParseBool(os.Getenv(workerEnabled))
	if args[0] != appBinary || len(args) != 1 || !bundled {
		path, err := exec.LookPath(args[0])
		if err != nil {
			return err
		}
		return syscall.Exec(path, args, os.Environ())
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	app := exec.Command(args[0])
	app.Stdin = os.Stdin
	s := supervisor{
		appCommand: app,
		workerCommand: func() *exec.Cmd {
			cmd := exec.Command("/usr/local/bin/bun", "--no-env-file", "/app/worker/index.ts")
			cmd.Env = workerEnvironment()
			return cmd
		},
		readinessURL:     workerReady,
		readinessTimeout: 10 * time.Second,
		retryDelay:       time.Second,
		retryWindow:      time.Minute,
		restartLimit:     3,
		// Sub2API allows 5s for HTTP shutdown and tracks deferred cleanup against a 10s deadline.
		appShutdownTimeout:    20 * time.Second,
		workerShutdownTimeout: 2 * time.Second,
	}
	return s.run(ctx)
}

func workerEnvironment() []string {
	env := []string{
		"PATH=/usr/local/bin:/usr/bin:/bin",
		"HOME=/home/sub2api",
		"LISTEN_HOST=" + workerHost,
		"PORT=" + workerPort,
		"BUN_RUNTIME_TRANSPILER_CACHE_PATH=0",
	}
	for _, key := range []string{workerToken, "TZ", "SSL_CERT_FILE", "SSL_CERT_DIR", "NODE_EXTRA_CA_CERTS"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

type supervisor struct {
	appCommand            *exec.Cmd
	workerCommand         func() *exec.Cmd
	readinessURL          string
	readinessTimeout      time.Duration
	retryDelay            time.Duration
	retryWindow           time.Duration
	restartLimit          int
	appShutdownTimeout    time.Duration
	workerShutdownTimeout time.Duration
}

func (s supervisor) run(ctx context.Context) error {
	var app, worker *process
	defer func() {
		// Active Sub2API requests may still need the worker while Go drains.
		app.stop(s.appShutdownTimeout)
		worker.stop(s.workerShutdownTimeout)
	}()

	var appDone <-chan struct{}
	var restarts []time.Time
	for ctx.Err() == nil {
		var err error
		worker, err = start(s.workerCommand())
		if err != nil {
			return fmt.Errorf("start worker: %w", err)
		}
		if err := s.waitForWorker(ctx, worker, appDone); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if app != nil && app.exited() {
				return app.exitError("Sub2API")
			}
			return err
		}
		log.Println("worker ready")

		if app == nil {
			if ctx.Err() != nil {
				return nil
			}
			app, err = start(s.appCommand)
			if err != nil {
				return fmt.Errorf("start Sub2API: %w", err)
			}
			appDone = app.done
		}
		select {
		case <-ctx.Done():
			return nil
		case <-appDone:
			return app.exitError("Sub2API")
		case <-worker.done:
			log.Print(worker.exitError("worker"))
		}

		now := time.Now()
		for len(restarts) > 0 && now.Sub(restarts[0]) >= s.retryWindow {
			restarts = restarts[1:]
		}
		if len(restarts) >= s.restartLimit {
			return fmt.Errorf("worker restart limit reached (%d within %s)", s.restartLimit, s.retryWindow)
		}
		restarts = append(restarts, now)
		log.Printf("restarting worker (%d/%d)", len(restarts), s.restartLimit)
		select {
		case <-ctx.Done():
			return nil
		case <-appDone:
			return app.exitError("Sub2API")
		case <-time.After(s.retryDelay):
		}
	}
	return nil
}

func (s supervisor) waitForWorker(ctx context.Context, worker *process, appDone <-chan struct{}) error {
	ctx, cancel := context.WithTimeout(ctx, s.readinessTimeout)
	defer cancel()
	client := &http.Client{
		Timeout:   time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.readinessURL, nil)
		if err != nil {
			return fmt.Errorf("create worker health request: %w", err)
		}
		if response, err := client.Do(req); err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK && !worker.exited() {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("worker readiness: %w", ctx.Err())
		case <-worker.done:
			return worker.exitError("worker before readiness")
		case <-appDone:
			return fmt.Errorf("Sub2API exited while waiting for worker")
		case <-tick.C:
		}
	}
}

type process struct {
	cmd  *exec.Cmd
	done chan struct{}
}

func start(cmd *exec.Cmd) (*process, error) {
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	child := &process{cmd: cmd, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(child.done)
	}()
	return child, nil
}

func (p *process) exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

func (p *process) exitError(name string) error {
	return fmt.Errorf("%s exited unexpectedly (%s)", name, p.cmd.ProcessState)
}

func (p *process) stop(timeout time.Duration) {
	if p == nil || p.exited() {
		return
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(timeout):
		log.Printf("%s did not stop within %s; sending SIGKILL", p.cmd.Path, timeout)
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}
