package workflow

import (
	"errors"

	"github.com/JamesbbBriz/agent-workflow/internal/contract"
	contractsv1 "github.com/JamesbbBriz/agent-workflow/pkg/contractsv1"
)

type IsolatedProvider interface {
	Provider
	IsolationEvidence() contractsv1.ProviderIsolationEvidence
}

func (e *Engine) RequireProviderIsolation(profile contractsv1.ProviderIsolationProfile) *Engine {
	e.requiredIsolation = profile
	return e
}

func (e *Engine) providerIsolation() (contractsv1.ProviderIsolationEvidence, error) {
	if e == nil || e.provider == nil {
		return contractsv1.ProviderIsolationEvidence{}, errors.New("provider is required")
	}
	evidence := contractsv1.ProviderIsolationEvidence{
		Kind: contractsv1.ProviderIsolationEvidenceKindProviderIsolationEvidence, SchemaVersion: 1,
		Profile:             contractsv1.ProviderIsolationProfileTrustedInProcess,
		Driver:              contractsv1.ProviderIsolationEvidenceDriverInProcess,
		DeclaredEnvironment: []string{},
	}
	if isolated, ok := e.provider.(IsolatedProvider); ok {
		evidence = isolated.IsolationEvidence()
	} else {
		var err error
		evidence, err = sealProviderIsolation(evidence)
		if err != nil {
			return contractsv1.ProviderIsolationEvidence{}, err
		}
	}
	if err := verifyProviderIsolation(evidence); err != nil {
		return contractsv1.ProviderIsolationEvidence{}, err
	}
	required := e.requiredIsolation
	if required == "" {
		required = contractsv1.ProviderIsolationProfileTrustedInProcess
	}
	switch required {
	case contractsv1.ProviderIsolationProfileTrustedInProcess:
	case contractsv1.ProviderIsolationProfileStagedSubprocess:
		if evidence.Profile != required {
			return contractsv1.ProviderIsolationEvidence{}, errors.New("production execution requires staged subprocess isolation")
		}
	default:
		return contractsv1.ProviderIsolationEvidence{}, errors.New("unknown required provider isolation profile")
	}
	return evidence, nil
}

func (e *Engine) validateInvocationIsolation(invocation Invocation) error {
	if invocation.Isolation == nil {
		return errors.New("legacy invocation without provider isolation is read-only")
	}
	if err := verifyProviderIsolation(*invocation.Isolation); err != nil {
		return err
	}
	current, err := e.providerIsolation()
	if err != nil {
		return err
	}
	if current.EvidenceHash != invocation.Isolation.EvidenceHash {
		return errors.New("provider isolation evidence changed after invocation admission")
	}
	return nil
}

func sealProviderIsolation(evidence contractsv1.ProviderIsolationEvidence) (contractsv1.ProviderIsolationEvidence, error) {
	if evidence.DeclaredEnvironment == nil {
		evidence.DeclaredEnvironment = []string{}
	}
	evidence.EvidenceHash = ""
	hash, err := providerIsolationDigest(evidence)
	if err != nil {
		return contractsv1.ProviderIsolationEvidence{}, err
	}
	evidence.EvidenceHash = contractsv1.SHA256(hash)
	if err := verifyProviderIsolation(evidence); err != nil {
		return contractsv1.ProviderIsolationEvidence{}, err
	}
	return evidence, nil
}

func verifyProviderIsolation(evidence contractsv1.ProviderIsolationEvidence) error {
	if err := contract.ValidateDefinition("ProviderIsolationEvidence", evidence); err != nil {
		return err
	}
	switch evidence.Profile {
	case contractsv1.ProviderIsolationProfileTrustedInProcess:
		if evidence.Driver != contractsv1.ProviderIsolationEvidenceDriverInProcess || evidence.ExecutableSha256 != nil || evidence.StagedRootSha256 != nil || len(evidence.DeclaredEnvironment) != 0 {
			return errors.New("trusted in-process isolation evidence is inconsistent")
		}
	case contractsv1.ProviderIsolationProfileStagedSubprocess:
		if evidence.Driver == contractsv1.ProviderIsolationEvidenceDriverInProcess || evidence.ExecutableSha256 == nil || evidence.StagedRootSha256 == nil {
			return errors.New("staged subprocess isolation evidence is incomplete")
		}
	default:
		return errors.New("unknown provider isolation profile")
	}
	want, err := providerIsolationDigest(evidence)
	if err != nil {
		return err
	}
	if evidence.EvidenceHash != contractsv1.SHA256(want) {
		return errors.New("provider isolation evidence hash is invalid")
	}
	return nil
}

func providerIsolationDigest(evidence contractsv1.ProviderIsolationEvidence) (string, error) {
	evidence.EvidenceHash = ""
	return Digest(evidence)
}
