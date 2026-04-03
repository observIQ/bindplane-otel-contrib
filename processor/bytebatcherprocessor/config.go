package bytebatcherprocessor

import (
	"errors"
	"time"
)

type Config struct {
	FlushInterval time.Duration `mapstructure:"flush_interval"`
	Bytes         int           `mapstructure:"bytes"`
}

func (c *Config) Validate() error {
	if c.FlushInterval <= 0 {
		return errors.New("flush_interval must be greater than 0")
	}
	if c.Bytes <= 0 {
		return errors.New("bytes must be greater than 0")
	}
	return nil
}
