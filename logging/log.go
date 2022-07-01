package logging

import (
	"go.uber.org/zap"
)

var log *zap.Logger

// Log returns the logger object
func Log() *zap.Logger {
	if log == nil {
		log, _ = zap.NewDevelopmentConfig().Build()
	}
	return log
}
