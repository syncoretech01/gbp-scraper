package web

import (
	"context"
	"errors"
	"time"
)

var (
	ErrProxyStoreUnsupported = errors.New("proxy storage is unavailable")
	ErrProxyNotFound         = errors.New("proxy not found")
)

type ProxyPoolRecord struct {
	ID           string
	Name         string
	Strategy     string
	TotalCount   int64
	EnabledCount int64
	HealthyCount int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type ProxyRecord struct {
	ID            string
	PoolID        string
	PoolName      string
	MaskedURL     string
	Protocol      string
	Enabled       bool
	Status        string
	LatencyMS     *int64
	SuccessCount  int64
	FailureCount  int64
	BlockCount    int64
	UsageCount    int64
	LastSuccessAt *time.Time
	LastFailureAt *time.Time
	CooldownUntil *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type ProxyTestResult struct {
	Status    string
	LatencyMS *int64
	ExitIP    string
	Country   string
	Error     string
	CheckedAt time.Time
}

type proxyRepository interface {
	ListProxyPools(context.Context) ([]ProxyPoolRecord, error)
	ListProxies(context.Context, string) ([]ProxyRecord, error)
	ImportProxyPool(context.Context, string, string, []string) (ProxyPoolRecord, int, error)
	ResolveProxyPool(context.Context, string) ([]string, error)
	GetProxySecret(context.Context, string) (string, error)
	RecordProxyTest(context.Context, string, ProxyTestResult) error
	SetProxyEnabled(context.Context, string, bool) error
	DeleteProxy(context.Context, string) error
	DeleteProxyPool(context.Context, string) error
}

func (s *Service) proxyRepository() (proxyRepository, error) {
	repository, ok := s.repo.(proxyRepository)
	if !ok {
		return nil, ErrProxyStoreUnsupported
	}
	return repository, nil
}

func (s *Service) ListProxyPools(ctx context.Context) ([]ProxyPoolRecord, error) {
	repository, err := s.proxyRepository()
	if err != nil {
		return nil, err
	}
	return repository.ListProxyPools(ctx)
}

func (s *Service) ListProxies(ctx context.Context, poolID string) ([]ProxyRecord, error) {
	repository, err := s.proxyRepository()
	if err != nil {
		return nil, err
	}
	return repository.ListProxies(ctx, poolID)
}

func (s *Service) ImportProxyPool(ctx context.Context, name, strategy string, values []string) (ProxyPoolRecord, int, error) {
	repository, err := s.proxyRepository()
	if err != nil {
		return ProxyPoolRecord{}, 0, err
	}
	return repository.ImportProxyPool(ctx, name, strategy, values)
}

func (s *Service) ResolveProxyPool(ctx context.Context, id string) ([]string, error) {
	repository, err := s.proxyRepository()
	if err != nil {
		return nil, err
	}
	return repository.ResolveProxyPool(ctx, id)
}

func (s *Service) GetProxySecret(ctx context.Context, id string) (string, error) {
	repository, err := s.proxyRepository()
	if err != nil {
		return "", err
	}
	return repository.GetProxySecret(ctx, id)
}

func (s *Service) RecordProxyTest(ctx context.Context, id string, result ProxyTestResult) error {
	repository, err := s.proxyRepository()
	if err != nil {
		return err
	}
	return repository.RecordProxyTest(ctx, id, result)
}

func (s *Service) SetProxyEnabled(ctx context.Context, id string, enabled bool) error {
	repository, err := s.proxyRepository()
	if err != nil {
		return err
	}
	return repository.SetProxyEnabled(ctx, id, enabled)
}

func (s *Service) DeleteProxy(ctx context.Context, id string) error {
	repository, err := s.proxyRepository()
	if err != nil {
		return err
	}
	return repository.DeleteProxy(ctx, id)
}

func (s *Service) DeleteProxyPool(ctx context.Context, id string) error {
	repository, err := s.proxyRepository()
	if err != nil {
		return err
	}
	return repository.DeleteProxyPool(ctx, id)
}
