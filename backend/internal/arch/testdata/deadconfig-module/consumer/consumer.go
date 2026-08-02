package consumer

import "example.com/deadconfig/config"

func Addr(c config.Config) string { return c.Server.Addr }
