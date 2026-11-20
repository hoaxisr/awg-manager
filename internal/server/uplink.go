package server

import "github.com/hoaxisr/awg-manager/internal/api"

// SetUplinkHandler must be called before Start builds authenticated routes.
func (s *Server) SetUplinkHandler(h *api.UplinkHandler) { s.uplinkHandler = h }
