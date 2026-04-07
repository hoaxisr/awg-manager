package dnsroute

import "context"

// Service manages DNS-based domain routing lists.
type Service interface {
	Create(ctx context.Context, list DomainList) (*DomainList, error)
	Get(ctx context.Context, id string) (*DomainList, error)
	List(ctx context.Context) ([]DomainList, error)
	Update(ctx context.Context, list DomainList) (*DomainList, error)
	Delete(ctx context.Context, id string) error
	DeleteBatch(ctx context.Context, ids []string) (int, error)
	CreateBatch(ctx context.Context, lists []DomainList) ([]*DomainList, error)
	SetEnabled(ctx context.Context, id string, enabled bool) error
	RefreshSubscriptions(ctx context.Context, id string) error
	RefreshAllSubscriptions(ctx context.Context) error
	Reconcile(ctx context.Context) error
}
