package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

const defaultProviderOutputLimit = 1 << 20

var environmentName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

type SubprocessProviderConfig struct {
	Executable string
	Args       []string
	// StagedRoot contains input/ and output/. Input is mounted read-only;
	// writes are confined to output.
	StagedRoot     string
	Environment    map[string]string
	MaxOutputBytes int
}

type SubprocessProvider struct {
	config   SubprocessProviderConfig
	evidence contractsv1.ProviderIsolationEvidence
	mu       sync.Mutex
	results  map[string]ProviderResult
	cancels  map[string]context.CancelFunc
	runs     map[string]*subprocessRun
}

type subprocessRun struct {
	done chan struct{}
	err  error
}

func NewSubprocessProvider(config SubprocessProviderConfig) (*SubprocessProvider, error) {
	executable, err := filepath.EvalSymlinks(config.Executable)
	if err != nil {
		return nil, fmt.Errorf("resolve provider executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return nil, errors.New("provider executable must be an executable regular file")
	}
	root, err := filepath.EvalSymlinks(config.StagedRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve staged root: %w", err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return nil, errors.New("provider staged root must be a directory")
	}
	for _, child := range []string{"input", "output"} {
		if info, err := os.Stat(filepath.Join(root, child)); err != nil || !info.IsDir() {
			return nil, errors.New("provider staged root requires input and output directories")
		}
	}
	for name, value := range config.Environment {
		if !environmentName.MatchString(name) || strings.ContainsRune(value, 0) {
			return nil, errors.New("provider environment declaration is invalid")
		}
	}
	if config.MaxOutputBytes == 0 {
		config.MaxOutputBytes = defaultProviderOutputLimit
	}
	if config.MaxOutputBytes < 1 || config.MaxOutputBytes > 8<<20 {
		return nil, errors.New("provider output limit must be between 1 byte and 8 MiB")
	}
	config.Args = append([]string{}, config.Args...)
	environment := make(map[string]string, len(config.Environment))
	for name, value := range config.Environment {
		environment[name] = value
	}
	config.Environment = environment
	config.Executable, config.StagedRoot = executable, root
	executableHash, err := hashFile(executable)
	if err != nil {
		return nil, err
	}
	rootHash, err := hashStagedRoot(root)
	if err != nil {
		return nil, err
	}
	driver, err := sandboxDriver()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(config.Environment))
	for name := range config.Environment {
		names = append(names, name)
	}
	sort.Strings(names)
	evidence, err := sealProviderIsolation(contractsv1.ProviderIsolationEvidence{
		Kind: contractsv1.ProviderIsolationEvidenceKindProviderIsolationEvidence, SchemaVersion: 1,
		Profile: contractsv1.ProviderIsolationProfileStagedSubprocess, Driver: driver,
		ExecutableSha256: &executableHash, StagedRootSha256: &rootHash, DeclaredEnvironment: names,
	})
	if err != nil {
		return nil, err
	}
	return &SubprocessProvider{config: config, evidence: evidence, results: map[string]ProviderResult{}, cancels: map[string]context.CancelFunc{}, runs: map[string]*subprocessRun{}}, nil
}

func (p *SubprocessProvider) IsolationEvidence() contractsv1.ProviderIsolationEvidence {
	evidence := p.evidence
	evidence.DeclaredEnvironment = append([]string{}, p.evidence.DeclaredEnvironment...)
	if p.evidence.ExecutableSha256 != nil {
		value := *p.evidence.ExecutableSha256
		evidence.ExecutableSha256 = &value
	}
	if p.evidence.StagedRootSha256 != nil {
		value := *p.evidence.StagedRootSha256
		evidence.StagedRootSha256 = &value
	}
	return evidence
}

func (p *SubprocessProvider) Start(ctx context.Context, invocation Invocation) error {
	if invocation.IdempotencyKey == "" || invocation.Deadline.IsZero() {
		return errors.New("provider invocation identity and deadline are required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	if _, ok := p.results[invocation.IdempotencyKey]; ok {
		p.mu.Unlock()
		return nil
	}
	if running := p.runs[invocation.IdempotencyKey]; running != nil {
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()
	if err := p.verifyInputs(); err != nil {
		return err
	}
	input, err := json.Marshal(invocation)
	if err != nil {
		return err
	}
	name, args, err := sandboxCommand(p.config.Executable, p.config.Args, p.config.StagedRoot)
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithDeadline(context.Background(), invocation.Deadline)
	cmd := exec.CommandContext(runCtx, name, args...)
	cmd.Dir = p.config.StagedRoot
	cmd.Env = make([]string, 0, len(p.config.Environment))
	for name, value := range p.config.Environment {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
	sort.Strings(cmd.Env)
	cmd.Stdin = bytes.NewReader(input)
	stdout, stderr := &limitedBuffer{limit: p.config.MaxOutputBytes}, &limitedBuffer{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	running := &subprocessRun{done: make(chan struct{})}
	p.mu.Lock()
	if _, exists := p.runs[invocation.IdempotencyKey]; exists {
		p.mu.Unlock()
		cancel()
		return nil
	}
	p.runs[invocation.IdempotencyKey] = running
	p.cancels[invocation.IdempotencyKey] = cancel
	p.mu.Unlock()
	go p.execute(invocation, cmd, stdout, running, cancel)
	return nil
}

func (p *SubprocessProvider) execute(invocation Invocation, cmd *exec.Cmd, stdout *limitedBuffer, running *subprocessRun, cancel context.CancelFunc) {
	err := cmd.Run()
	if err == nil {
		var result ProviderResult
		decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
		decoder.UseNumber()
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&result); err != nil {
			err = fmt.Errorf("decode provider result: %w", err)
		}
		if err == nil {
			err = ensureJSONEOF(decoder)
		}
		if err == nil && result.IdempotencyKey != invocation.IdempotencyKey {
			err = errors.New("provider result idempotency key does not match invocation")
		}
		if err == nil {
			p.mu.Lock()
			p.results[invocation.IdempotencyKey] = result
			p.mu.Unlock()
		}
	} else {
		err = fmt.Errorf("isolated provider subprocess failed: %w", err)
	}
	cancel()
	p.mu.Lock()
	running.err = err
	delete(p.cancels, invocation.IdempotencyKey)
	close(running.done)
	p.mu.Unlock()
}

func (p *SubprocessProvider) Poll(_ context.Context, key string) (ProviderResult, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	result, ok := p.results[key]
	if ok {
		return result, true, nil
	}
	running := p.runs[key]
	if running == nil {
		return ProviderResult{}, false, nil
	}
	select {
	case <-running.done:
		if running.err == nil {
			return ProviderResult{}, false, errors.New("provider subprocess completed without a result")
		}
		if errors.Is(running.err, context.DeadlineExceeded) || errors.Is(running.err, context.Canceled) || strings.Contains(running.err.Error(), "signal: killed") {
			return ProviderResult{}, false, nil
		}
		return ProviderResult{}, false, running.err
	default:
		return ProviderResult{}, false, nil
	}
}

func (p *SubprocessProvider) Cancel(_ context.Context, key string) error {
	p.mu.Lock()
	cancel := p.cancels[key]
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (p *SubprocessProvider) verifyInputs() error {
	executableHash, err := hashFile(p.config.Executable)
	if err != nil {
		return err
	}
	rootHash, err := hashStagedRoot(p.config.StagedRoot)
	if err != nil {
		return err
	}
	if p.evidence.ExecutableSha256 == nil || p.evidence.StagedRootSha256 == nil || *p.evidence.ExecutableSha256 != executableHash || *p.evidence.StagedRootSha256 != rootHash {
		return errors.New("provider executable or staged root changed after isolation admission")
	}
	return nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, errors.New("provider output exceeded its limit")
	}
	return b.Buffer.Write(p)
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("provider output contains more than one JSON value")
		}
		return fmt.Errorf("decode provider output trailer: %w", err)
	}
	return nil
}

func hashFile(path string) (contractsv1.SHA256, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return contractsv1.SHA256("sha256:" + hex.EncodeToString(hash.Sum(nil))), nil
}

func hashStagedRoot(root string) (contractsv1.SHA256, error) {
	hash := sha256.New()
	outputRoot := filepath.Join(root, "output")
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("provider staged root cannot contain symlinks")
		}
		if path != outputRoot && strings.HasPrefix(path, outputRoot+string(filepath.Separator)) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if _, err := io.WriteString(hash, relative+"\x00"+info.Mode().String()+"\x00"); err != nil || !info.Mode().IsRegular() {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		return "", err
	}
	return contractsv1.SHA256("sha256:" + hex.EncodeToString(hash.Sum(nil))), nil
}

var _ IsolatedProvider = (*SubprocessProvider)(nil)
