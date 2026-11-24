package service

import "context"

func (s *ServiceImpl) SetUplinkMutationGuard(guard func(context.Context, string, string) (func(), error)) {
	s.uplinkMutation = guard
}

// GuardUplinkMutation — тот же страж uplink для путей, что зовут оркестратор
// напрямую (кнопки карточки, MCP, политики доступа): инвариант «туннель
// политики не меняют посреди её сверки» держится на всех входах.
func (s *ServiceImpl) GuardUplinkMutation(ctx context.Context, id, operation string) (func(), error) {
	return s.beginUplinkMutation(ctx, id, operation)
}

func (s *ServiceImpl) beginUplinkMutation(ctx context.Context, id, operation string) (func(), error) {
	if s.uplinkMutation == nil {
		return func() {}, nil
	}
	return s.uplinkMutation(ctx, id, operation)
}
