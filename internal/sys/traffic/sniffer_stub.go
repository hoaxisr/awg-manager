//go:build !linux

package traffic

import "context"

func (s *Service) startLiveSniffer(ctx context.Context, gen uint64) {
	s.snifferWg.Add(1)
	go func() {
		defer s.snifferWg.Done()
		defer func() {
			s.snifferMu.Lock()
			if s.snifferGen == gen {
				s.snifferActive = false
				s.snifferCancel = nil
			}
			s.snifferMu.Unlock()
		}()
		if s.openPacketSocket != nil {
			fd, err := s.openPacketSocket()
			if err != nil {
				return
			}
			defer func() {
				if s.closePacketSocket != nil {
					_ = s.closePacketSocket(fd)
				}
			}()
			_ = fd
			<-ctx.Done()
		}
	}()
}
