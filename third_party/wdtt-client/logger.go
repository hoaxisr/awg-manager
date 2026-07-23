package main

import "github.com/pion/logging"

type NullLoggerFactory struct{}

func (n *NullLoggerFactory) NewLogger(_ string) logging.LeveledLogger { return &NullLogger{} }

type NullLogger struct{}

func (n *NullLogger) Trace(_ string)                    {}
func (n *NullLogger) Tracef(_ string, _ ...interface{}) {}
func (n *NullLogger) Debug(_ string)                    {}
func (n *NullLogger) Debugf(_ string, _ ...interface{}) {}
func (n *NullLogger) Info(_ string)                     {}
func (n *NullLogger) Infof(_ string, _ ...interface{})  {}
func (n *NullLogger) Warn(_ string)                     {}
func (n *NullLogger) Warnf(_ string, _ ...interface{})  {}
func (n *NullLogger) Error(_ string)                    {}
func (n *NullLogger) Errorf(_ string, _ ...interface{}) {}
