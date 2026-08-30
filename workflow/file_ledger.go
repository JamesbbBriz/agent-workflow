package workflow

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

type FileLedger struct {
	mu       sync.Mutex
	path     string
	readOnly bool
}

var ErrCanonicalConflict = errors.New("canonical receipt conflict")

func OpenFileLedger(path string) (*FileLedger, error) {
	return openFileLedger(path, false)
}

func OpenFileLedgerReadOnly(path string) (*FileLedger, error) {
	return openFileLedger(path, true)
}

func openFileLedger(path string, readOnly bool) (*FileLedger, error) {
	if path == "" {
		return nil, errors.New("ledger path is required")
	}
	path = filepath.Clean(path)
	if !readOnly {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create ledger directory: %w", err)
		}
	}
	directoryInfo, err := os.Stat(filepath.Dir(path))
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("ledger directory must be a private trusted directory")
	}
	info, statErr := os.Lstat(path)
	created := errors.Is(statErr, os.ErrNotExist)
	if created && readOnly {
		return nil, errors.New("ledger does not exist")
	}
	if statErr != nil && !created {
		return nil, fmt.Errorf("inspect ledger: %w", statErr)
	}
	if !created && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return nil, errors.New("ledger must be a regular file, not a symlink")
	}
	flags := os.O_RDONLY
	if !readOnly {
		flags = os.O_CREATE | os.O_APPEND | os.O_WRONLY
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open ledger: %w", err)
	}
	if !readOnly {
		if err := file.Chmod(0o600); err != nil {
			file.Close()
			return nil, fmt.Errorf("secure ledger: %w", err)
		}
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close ledger: %w", err)
	}
	if created {
		directory, err := os.Open(filepath.Dir(path))
		if err != nil {
			return nil, fmt.Errorf("open ledger directory: %w", err)
		}
		if err := directory.Sync(); err != nil {
			directory.Close()
			return nil, fmt.Errorf("sync ledger directory: %w", err)
		}
		if err := directory.Close(); err != nil {
			return nil, fmt.Errorf("close ledger directory: %w", err)
		}
	}
	ledger := &FileLedger{path: path, readOnly: readOnly}
	lock, err := ledger.lock(!readOnly)
	if err != nil {
		return nil, err
	}
	defer unlockLedger(lock)
	if !readOnly {
		if err := recoverTornTail(path); err != nil {
			return nil, err
		}
	}
	if _, err := ledger.load(); err != nil {
		return nil, err
	}
	return ledger, nil
}

func (l *FileLedger) lock(exclusive bool) (*os.File, error) {
	if l.readOnly {
		return lockExistingLedger(l.path, exclusive)
	}
	return lockLedger(l.path, exclusive)
}

func recoverTornTail(path string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("inspect ledger tail: %w", err)
	}
	if len(body) == 0 || body[len(body)-1] == '\n' {
		return nil
	}
	start := bytes.LastIndexByte(body, '\n') + 1
	var receipt contractsv1.Receipt
	if err := contract.DecodeDefinition("Receipt", body[start:], &receipt); err == nil {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("recover ledger tail: %w", err)
		}
		if _, err := file.Write([]byte{'\n'}); err != nil {
			file.Close()
			return fmt.Errorf("recover ledger tail: %w", err)
		}
		if err := file.Sync(); err != nil {
			file.Close()
			return fmt.Errorf("sync recovered ledger: %w", err)
		}
		return file.Close()
	}
	file, err := os.OpenFile(path, os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("recover ledger tail: %w", err)
	}
	if err := file.Truncate(int64(start)); err != nil {
		file.Close()
		return fmt.Errorf("truncate torn ledger tail: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync truncated ledger: %w", err)
	}
	return file.Close()
}

func (l *FileLedger) Append(receipt contractsv1.Receipt) error {
	return l.AppendBatch([]contractsv1.Receipt{receipt})
}

func (l *FileLedger) AppendBatch(receipts []contractsv1.Receipt) error {
	return l.appendBatch(receipts, nil)
}

func (l *FileLedger) AppendAdmission(receipt contractsv1.Receipt, job contractsv1.JobDefinition, campaign contractsv1.CampaignDefinition) error {
	return l.appendBatch([]contractsv1.Receipt{receipt}, func(all map[string][]contractsv1.Receipt) error {
		return validateAdmissionDefinitionBindings(all, job, campaign)
	})
}

func (l *FileLedger) appendBatch(receipts []contractsv1.Receipt, validate func(map[string][]contractsv1.Receipt) error) error {
	if l.readOnly {
		return errors.New("ledger is read-only")
	}
	if len(receipts) == 0 {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	lock, err := l.lock(true)
	if err != nil {
		return err
	}
	defer unlockLedger(lock)
	all, err := l.load()
	if err != nil {
		return err
	}
	if validate != nil {
		if err := validate(all); err != nil {
			return err
		}
	}
	missing := make([]contractsv1.Receipt, 0, len(receipts))
	for _, receipt := range receipts {
		if receipt.AggregateVersion < 1 {
			return errors.New("receipt aggregate version must be positive")
		}
		current := all[receipt.AggregateId]
		index := receipt.AggregateVersion - 1
		if index < len(current) {
			if current[index].ReceiptHash != receipt.ReceiptHash {
				return fmt.Errorf("%w: receipt version %d conflicts with canonical history", ErrCanonicalConflict, receipt.AggregateVersion)
			}
			continue
		}
		if err := validateNextReceipt(current, receipt); err != nil {
			return err
		}
		all[receipt.AggregateId] = append(current, receipt)
		missing = append(missing, receipt)
	}
	if len(missing) == 0 {
		return nil
	}
	body, err := os.ReadFile(l.path)
	if err != nil {
		return fmt.Errorf("read ledger for atomic append: %w", err)
	}
	for _, receipt := range missing {
		record, err := json.Marshal(receipt)
		if err != nil {
			return fmt.Errorf("encode receipt: %w", err)
		}
		body = append(body, record...)
		body = append(body, '\n')
	}
	temporary, err := os.CreateTemp(filepath.Dir(l.path), ".ledger-*.tmp")
	if err != nil {
		return fmt.Errorf("create atomic ledger: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure atomic ledger: %w", err)
	}
	if written, err := temporary.Write(body); err != nil || written != len(body) {
		temporary.Close()
		if err == nil {
			err = errors.New("short write")
		}
		return fmt.Errorf("write atomic ledger: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync atomic ledger: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close atomic ledger: %w", err)
	}
	if err := os.Rename(temporaryPath, l.path); err != nil {
		return fmt.Errorf("commit atomic ledger: %w", err)
	}
	directory, err := os.Open(filepath.Dir(l.path))
	if err != nil {
		return fmt.Errorf("open ledger directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync ledger directory: %w", err)
	}
	return nil
}

func (l *FileLedger) Replay(aggregateID string) (contractsv1.ReplayBundle, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lock, err := l.lock(false)
	if err != nil {
		return contractsv1.ReplayBundle{}, err
	}
	defer unlockLedger(lock)
	all, err := l.load()
	if err != nil {
		return contractsv1.ReplayBundle{}, err
	}
	return replayBundle(aggregateID, all[aggregateID])
}

func (l *FileLedger) ReplaysByReceiptType(receiptType contractsv1.ReceiptReceiptType) ([]contractsv1.ReplayBundle, error) {
	return l.ReplaysByReceiptTypes(receiptType)
}

func (l *FileLedger) ReplaysByReceiptTypes(receiptTypes ...contractsv1.ReceiptReceiptType) ([]contractsv1.ReplayBundle, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lock, err := l.lock(false)
	if err != nil {
		return nil, err
	}
	defer unlockLedger(lock)
	all, err := l.load()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(all))
	accepted := make(map[contractsv1.ReceiptReceiptType]bool, len(receiptTypes))
	for _, receiptType := range receiptTypes {
		accepted[receiptType] = true
	}
	for id, receipts := range all {
		matched := len(receipts) > 0 && len(accepted) > 0
		for _, receipt := range receipts {
			matched = matched && accepted[receipt.ReceiptType]
		}
		if matched {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	replays := make([]contractsv1.ReplayBundle, 0, len(ids))
	for _, id := range ids {
		replay, err := replayBundle(id, all[id])
		if err != nil {
			return nil, err
		}
		replays = append(replays, replay)
	}
	return replays, nil
}

func (l *FileLedger) load() (map[string][]contractsv1.Receipt, error) {
	file, err := os.Open(l.path)
	if err != nil {
		return nil, fmt.Errorf("read ledger: %w", err)
	}
	defer file.Close()
	all := make(map[string][]contractsv1.Receipt)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), contract.MaxDocumentBytes+1)
	line := 0
	for scanner.Scan() {
		line++
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			return nil, fmt.Errorf("ledger line %d is empty", line)
		}
		var receipt contractsv1.Receipt
		if err := contract.DecodeDefinition("Receipt", raw, &receipt); err != nil {
			return nil, fmt.Errorf("ledger line %d: %w", line, err)
		}
		current := all[receipt.AggregateId]
		if receipt.AggregateVersion <= len(current) {
			return nil, fmt.Errorf("ledger line %d duplicates aggregate version", line)
		}
		if err := validateNextReceipt(current, receipt); err != nil {
			return nil, fmt.Errorf("ledger line %d: %w", line, err)
		}
		all[receipt.AggregateId] = append(current, receipt)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan ledger: %w", err)
	}
	return all, nil
}

func validateNextReceipt(current []contractsv1.Receipt, receipt contractsv1.Receipt) error {
	if err := contract.ValidateDefinition("Receipt", receipt); err != nil {
		return err
	}
	if receipt.AggregateVersion != len(current)+1 {
		return fmt.Errorf("receipt version %d is not contiguous", receipt.AggregateVersion)
	}
	if len(current) == 0 {
		if receipt.PreviousReceiptHash != nil {
			return errors.New("first receipt has a predecessor")
		}
	} else if previousReceiptHash(receipt.PreviousReceiptHash) != current[len(current)-1].ReceiptHash {
		return errors.New("receipt previous hash does not match canonical history")
	}
	expected := receipt.ReceiptHash
	receipt.ReceiptHash = ""
	hash, err := receiptDigest(receipt)
	if err != nil || contractsv1.SHA256(hash) != expected {
		return fmt.Errorf("receipt hash does not match canonical content: got %s want %s", hash, expected)
	}
	return nil
}
