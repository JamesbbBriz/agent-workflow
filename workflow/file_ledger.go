package workflow

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

type FileLedger struct {
	mu   sync.Mutex
	path string
}

func OpenFileLedger(path string) (*FileLedger, error) {
	if path == "" {
		return nil, errors.New("ledger path is required")
	}
	path = filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create ledger directory: %w", err)
	}
	_, statErr := os.Stat(path)
	created := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !created {
		return nil, fmt.Errorf("inspect ledger: %w", statErr)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open ledger: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return nil, fmt.Errorf("secure ledger: %w", err)
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
	ledger := &FileLedger{path: path}
	if _, err := ledger.load(); err != nil {
		return nil, err
	}
	return ledger, nil
}

// ponytail: this is a single-writer crash-durable ledger; use a database or OS
// lock when multiple Core processes need to append to the same aggregate.
func (l *FileLedger) Append(receipt contractsv1.Receipt) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	all, err := l.load()
	if err != nil {
		return err
	}
	current := all[receipt.AggregateId]
	index := receipt.AggregateVersion - 1
	if index < len(current) {
		if current[index].ReceiptHash != receipt.ReceiptHash {
			return fmt.Errorf("receipt version %d conflicts with canonical history", receipt.AggregateVersion)
		}
		return nil
	}
	if err := validateNextReceipt(current, receipt); err != nil {
		return err
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("encode receipt: %w", err)
	}
	file, err := os.OpenFile(l.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("append receipt: %w", err)
	}
	defer file.Close()
	record := append(body, '\n')
	if written, err := file.Write(record); err != nil {
		return fmt.Errorf("write receipt: %w", err)
	} else if written != len(record) {
		return errors.New("write receipt: short write")
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync receipt: %w", err)
	}
	return nil
}

func (l *FileLedger) Replay(aggregateID string) (contractsv1.ReplayBundle, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	all, err := l.load()
	if err != nil {
		return contractsv1.ReplayBundle{}, err
	}
	return replayBundle(aggregateID, all[aggregateID])
}

func (l *FileLedger) load() (map[string][]contractsv1.Receipt, error) {
	file, err := os.Open(l.path)
	if err != nil {
		return nil, fmt.Errorf("read ledger: %w", err)
	}
	defer file.Close()
	all := make(map[string][]contractsv1.Receipt)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
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
	hash, err := Digest(receipt)
	if err != nil || contractsv1.SHA256(hash) != expected {
		return errors.New("receipt hash does not match canonical content")
	}
	return nil
}
