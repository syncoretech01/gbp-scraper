package web

import (
	"context"
	"errors"
	"fmt"
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

// ProxyPlan is what the task pool needs to assign proxies to tasks: the
// usable proxies in a stable order, the pool's rotation strategy, and an
// optional per-proxy task cap.
type ProxyPlan struct {
	PoolID           string
	Strategy         string
	Proxies          []string
	MaxTasksPerProxy int
}

type proxyRepository interface {
	ListProxyPools(context.Context) ([]ProxyPoolRecord, error)
	ListProxies(context.Context, string) ([]ProxyRecord, error)
	ImportProxyPool(context.Context, string, string, []string) (ProxyPoolRecord, int, error)
	ResolveProxyPool(context.Context, string) ([]string, error)
	ResolveProxyPlan(context.Context, string) (ProxyPlan, error)
	SetProxyPoolTaskCap(context.Context, string, int) error
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

// ResolveProxyPlan returns the proxies, strategy, and per-proxy task cap the
// worker needs for sticky assignment and limits.
func (s *Service) ResolveProxyPlan(ctx context.Context, id string) (ProxyPlan, error) {
	repository, err := s.proxyRepository()
	if err != nil {
		return ProxyPlan{}, err
	}

	return repository.ResolveProxyPlan(ctx, id)
}

// SetProxyPoolTaskCap bounds how many tasks one run may assign to a single
// proxy of the pool (0 removes the cap; at most 10,000).
func (s *Service) SetProxyPoolTaskCap(ctx context.Context, id string, taskCap int) error {
	repository, err := s.proxyRepository()
	if err != nil {
		return err
	}

	if taskCap < 0 || taskCap > 10_000 {
		return fmt.Errorf("per-proxy task cap must be between 0 and 10000")
	}

	return repository.SetProxyPoolTaskCap(ctx, id, taskCap)
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
