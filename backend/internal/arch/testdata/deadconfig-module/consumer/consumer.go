package consumer

import "example.com/deadconfig/config"

func Addr(c config.Config) string { return c.Server.Addr }

// Touch exercises non-read use shapes the checker must NOT count.
func Touch(c *config.Config) config.Config {
	c.OnlyAssigned = "written, never read"
	return config.Config{OnlyLiteralKey: "key position, not a read"}
}
