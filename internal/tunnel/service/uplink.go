package service

import "context"

func (s *ServiceImpl) SetUplinkMutationGuard(guard func(context.Context, string, string) (func(), error)) {
	s.uplinkMutation = guard
}
func (s *ServiceImpl) beginUplinkMutation(ctx context.Context, id, operation string) (func(), error) {
	if s.uplinkMutation == nil {
		return func() {}, nil
	}
	return s.uplinkMutation(ctx, id, operation)
}
